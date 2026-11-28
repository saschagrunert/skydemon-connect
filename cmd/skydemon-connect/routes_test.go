package main

import (
	"context"
	"log"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync/atomic"
	"testing"
	"testing/synctest"
	"time"

	"github.com/saschagrunert/skydemon-connect/internal/exexml"
	"github.com/saschagrunert/skydemon-connect/internal/gtn"
	"github.com/saschagrunert/skydemon-connect/internal/plugin"
	"github.com/saschagrunert/skydemon-connect/internal/position"
	"github.com/saschagrunert/skydemon-connect/internal/testutil"
)

func TestInstallPlugin(t *testing.T) {
	t.Parallel()

	user, packages := t.TempDir(), t.TempDir()

	err := os.Mkdir(filepath.Join(packages, "Community"), 0o700)
	if err != nil {
		t.Fatal(err)
	}

	err = os.WriteFile(filepath.Join(user, "UserCfg.opt"),
		[]byte(`InstalledPackagesPath "`+packages+`"`+"\r\n"), 0o600)
	if err != nil {
		t.Fatal(err)
	}

	var out testutil.Buffer

	l := log.New(&out, "", 0)
	installs := exexml.Installs{
		filepath.Join(user, "exe.xml"),
		filepath.Join(t.TempDir(), "exe.xml"),
	}

	installPlugin(installs, l, true)
	installPlugin(installs[:1], l, true)

	_, err = os.Stat(filepath.Join(packages, "Community", plugin.Name, "manifest.json"))
	if err != nil {
		t.Error(err)
	}

	for _, want := range []string{"installed in", "MSFS's settings", "up to date in"} {
		if !strings.Contains(out.String(), want) {
			t.Errorf("log without %q:\n%s", want, out.String())
		}
	}
}

// testEnv is a routeEnv with installs, MSFS running as running says, and
// the route server on a port of its own.
func testEnv(installs exexml.Installs, running func() bool) routeEnv {
	return routeEnv{
		installs: installs,
		running:  running,
		addr:     "127.0.0.1:0",
		retry:    10 * time.Millisecond,
		exitWait: 0,
	}
}

// notRunning is MSFS closed.
func notRunning() bool { return false }

// testLinks are the links of an app that is connected and doesn't stop
// because MSFS closed.
func testLinks(w *gtn.Writer) routeLinks {
	return routeLinks{gtn: w, msfsClosed: notRunning, connected: func() bool { return true }}
}

// oldPlugin makes an MSFS installation with an older plugin and returns it
// and the plugin's script.
func oldPlugin(t *testing.T) (exexml.Installs, string) {
	t.Helper()

	user, packages := t.TempDir(), t.TempDir()
	community := filepath.Join(packages, "Community")

	err := os.Mkdir(community, 0o700)
	if err != nil {
		t.Fatal(err)
	}

	err = os.WriteFile(filepath.Join(user, "UserCfg.opt"),
		[]byte(`InstalledPackagesPath "`+packages+`"`+"\r\n"), 0o600)
	if err != nil {
		t.Fatal(err)
	}

	installs := exexml.Installs{filepath.Join(user, "exe.xml")}
	installPlugin(installs, testutil.Log(t), true)

	script := filepath.Join(community, plugin.Name, "html_ui", "SkyDemonConnect",
		"G1000", "route-plugin.js")

	err = os.WriteFile(script, []byte("old"), 0o600)
	if err != nil {
		t.Fatal(err)
	}

	return installs, script
}

// An update found while MSFS runs waits until MSFS is closed, or until the
// app stops with MSFS.
func TestUpdatePluginLater(t *testing.T) {
	t.Parallel()

	for _, closed := range []string{"while running", "at the end"} {
		installs, script := oldPlugin(t)
		c := flags(t, "-routes", t.TempDir())

		var running atomic.Bool

		running.Store(true)

		ctx, cancel := context.WithCancel(t.Context())
		done := make(chan struct{})

		go func() {
			runRoutesWith(ctx, c, testLinks(nil), testutil.Log(t), testEnv(installs, running.Load))
			close(done)
		}()

		time.Sleep(50 * time.Millisecond)

		if b, _ := os.ReadFile(script); string(b) != "old" {
			t.Fatalf("%s: updated while MSFS runs", closed)
		}

		running.Store(false)

		if closed == "while running" {
			time.Sleep(50 * time.Millisecond)
		}

		cancel()
		<-done

		if b, _ := os.ReadFile(script); string(b) == "old" {
			t.Errorf("%s: not updated once MSFS was closed", closed)
		}
	}
}

// An update that fails, e.g. with files MSFS still held, is tried again.
func TestUpdatePluginRetries(t *testing.T) {
	t.Parallel()

	if runtime.GOOS == "windows" || os.Geteuid() == 0 {
		t.Skip("needs a folder that can't be written to")
	}

	installs, script := oldPlugin(t)
	community := filepath.Dir(filepath.Dir(filepath.Dir(filepath.Dir(filepath.Dir(script)))))

	// Nothing can be created in it: the update fails.
	err := os.Chmod(community, 0o500) //nolint:gosec // a folder: read and list only
	if err != nil {
		t.Fatal(err)
	}

	t.Cleanup(func() { _ = os.Chmod(community, 0o700) }) //nolint:gosec // a folder, as it was

	var out testutil.Buffer

	c := flags(t, "-routes", t.TempDir())
	ctx, cancel := context.WithCancel(t.Context())
	done := make(chan struct{})

	go func() {
		runRoutesWith(ctx, c, testLinks(nil), log.New(&out, "", 0), testEnv(installs, notRunning))
		close(done)
	}()

	time.Sleep(50 * time.Millisecond)

	err = os.Chmod(community, 0o700) //nolint:gosec // a folder, as it was
	if err != nil {
		t.Fatal(err)
	}

	time.Sleep(50 * time.Millisecond)
	cancel()
	<-done

	if b, _ := os.ReadFile(script); string(b) == "old" ||
		!strings.Contains(out.String(), "MSFS plugin: install") {
		t.Errorf("not updated after a failure:\n%s", out.String())
	}
}

// At the end, an update pending waits for MSFS to be gone only when the
// app stops because MSFS closed; a Quit with MSFS running leaves it for the
// next start at once.
func TestUpdatePluginAtEnd(t *testing.T) {
	t.Parallel()

	for _, closed := range []bool{true, false} {
		installs, script := oldPlugin(t)

		synctest.Test(t, func(t *testing.T) {
			env := testEnv(installs, nil)
			env.retry, env.exitWait = time.Hour, time.Second

			// MSFS takes 100 ms to close once the app stops.
			var stoppedAt time.Time

			env.running = func() bool {
				return stoppedAt.IsZero() || time.Since(stoppedAt) < 100*time.Millisecond
			}

			var out testutil.Buffer

			ctx, cancel := context.WithCancel(t.Context())
			done := make(chan struct{})

			go func() {
				updatePluginLater(ctx, log.New(&out, "", 0), env, func() bool { return closed })
				close(done)
			}()

			synctest.Wait()

			stoppedAt = time.Now()
			start := stoppedAt

			cancel()

			<-done

			b, _ := os.ReadFile(script)
			updated := string(b) != "old"

			// Closed: updated once MSFS is gone; else at once, not.
			took := time.Since(start)
			if updated != closed || (closed && took < 100*time.Millisecond) ||
				(!closed && took > 0) {
				t.Errorf("MSFS closed %v: updated %v after %v\n%s",
					closed, updated, took, out.String())
			}
		})
	}
}

// runRoutes serves until it is stopped.
func TestRunRoutes(t *testing.T) {
	t.Parallel()

	var out testutil.Buffer

	ctx, cancel := context.WithTimeout(t.Context(), 200*time.Millisecond)
	defer cancel()

	c := flags(t, "-routes", t.TempDir())
	runRoutesWith(ctx, c, testLinks(nil), log.New(&out, "", 0), testEnv(nil, notRunning))

	want := "route: routes saved in " + c.routes + " go to no avionics yet"
	if !strings.Contains(out.String(), want) || strings.Contains(out.String(), "listen") {
		t.Errorf("log:\n%s", out.String())
	}
}

// gone waits for MSFS to close, but not forever.
func TestGone(t *testing.T) {
	t.Parallel()

	calls := 0
	closing := func() bool {
		calls++

		return calls < 3
	}

	if !gone(closing, time.Second, time.Millisecond) || calls != 3 {
		t.Errorf("closing: %d calls", calls)
	}

	if gone(func() bool { return true }, 5*time.Millisecond, time.Millisecond) {
		t.Error("still running, yet gone")
	}
}

// With the TDS GTNXi, a saved route is written for it, also when another
// copy serves the G1000.
func TestRunRoutesGTN(t *testing.T) {
	t.Parallel()

	programData, routes := t.TempDir(), t.TempDir()
	fpl := filepath.Join(programData, "TDS", "GTNXi", "FPL")

	err := os.MkdirAll(fpl, 0o700)
	if err != nil {
		t.Fatal(err)
	}

	b, err := os.ReadFile("../../internal/flightplan/testdata/Padova - TrevisoS.Angelo.flightplan")
	if err != nil {
		t.Fatal(err)
	}

	err = os.WriteFile(filepath.Join(routes, "Padova - TrevisoS.Angelo.flightplan"), b, 0o600)
	if err != nil {
		t.Fatal(err)
	}

	var out testutil.Buffer

	l := log.New(&out, "", 0)

	if newGTN(t.TempDir(), l) != nil {
		t.Error("a GTN without the TDS GTNXi")
	}

	w := newGTN(programData, l)
	if w == nil {
		t.Fatal("no GTN")
	}

	w.SetAirports([]position.Airport{
		{Ident: "LIPU", Lat: 45.3958, Lon: 11.8478},
		{Ident: "LIPH", Lat: 45.6484, Lon: 12.1944},
	})

	ctx, cancel := context.WithTimeout(t.Context(), 500*time.Millisecond)
	defer cancel()

	runRoutesWith(ctx, flags(t, "-routes", routes), testLinks(w), l, testEnv(nil, notRunning))

	got, err := os.ReadFile(filepath.Join(fpl, gtn.Name+".gfp"))
	if err != nil || string(got) != "FPN/RI:F:LIPU,N45238E011509:F:LIPH,N45390E012119" {
		t.Errorf("plan %q, %v\n%s", got, err, out.String())
	}
}
