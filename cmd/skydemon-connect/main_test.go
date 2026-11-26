package main

import (
	"bytes"
	"context"
	"errors"
	"flag"
	"log"
	"net/netip"
	"slices"
	"strings"
	"testing"
	"testing/synctest"
	"time"

	"github.com/saschagrunert/skydemon-connect/internal/feed"
	"github.com/saschagrunert/skydemon-connect/internal/serial"
	"github.com/saschagrunert/skydemon-connect/internal/simconnect"
	"github.com/saschagrunert/skydemon-connect/internal/targets"
	"github.com/saschagrunert/skydemon-connect/internal/testutil"
)

func TestParseFlags(t *testing.T) {
	t.Parallel()

	c, _, err := parseFlags(nil)
	if err != nil || c.udp != "auto" || c.baud != 115200 || c.name != "MSFS" {
		t.Fatalf("defaults: %+v %v", c, err)
	}

	c, _, err = parseFlags([]string{"-udp", "127.0.0.1:49002", "-com", "COM11", "-auto"})
	if err != nil || c.udp != "127.0.0.1:49002" || c.com != "COM11" || !c.auto {
		t.Errorf("flags: %+v %v", c, err)
	}

	_, usage, err := parseFlags([]string{"-hz", "10"}) // removed: always 30 Hz
	if err == nil || !strings.Contains(usage, "-udp") ||
		strings.Count(usage, "flag provided but not defined") != 1 {
		t.Errorf("unknown flag: %v, usage %q", err, usage)
	}

	_, usage, err = parseFlags([]string{"-help"})
	if !errors.Is(err, flag.ErrHelp) || usage == "" {
		t.Errorf("-help: %v", err)
	}

	c, _, err = parseFlags([]string{"-version"})
	if err != nil || !c.version {
		t.Errorf("-version: %v %+v", err, c)
	}
}

func TestParseFlagsValidates(t *testing.T) {
	t.Parallel()

	for _, tc := range []struct {
		want error
		args []string
	}{
		{errArg, []string{"-udp", "192.168.1.20:49002,", "192.168.1.21:49002"}},
		{serial.ErrBaud, []string{"-baud", "0"}},
		{serial.ErrBaud, []string{"-baud", "4294967296"}},
		{errNoOut, []string{"-udp="}},
		{errNoOut, []string{"-udp", " , "}},
		{errName, []string{"-name", "MS,FS"}},
		{errName, []string{"-name="}},
		{errName, []string{"-name", "Flugsimulator für zu Hause"}},
		{errName, []string{"-name", strings.Repeat("x", maxName+1)}},
		{errDemoLat, []string{"-demo-lat", "90"}},
		{errDemoLat, []string{"-demo-lat", "NaN"}},
		{errDemoLon, []string{"-demo-lon", "-181"}},
	} {
		_, usage, err := parseFlags(tc.args)
		if !errors.Is(err, tc.want) ||
			!strings.Contains(strings.SplitN(usage, "\n", 2)[0], tc.want.Error()) ||
			!strings.Contains(usage, "-udp") {
			t.Errorf("%v: %v, usage %q", tc.args, err, usage)
		}
	}
}

// A bad -udp target is a usage error, not one at startup.
func TestParseFlagsValidatesUDP(t *testing.T) {
	t.Parallel()

	_, usage, err := parseFlags([]string{"-udp", "192.168.1.300:49002"})
	if err == nil || !strings.HasPrefix(usage, "-udp: ") {
		t.Errorf("%v, usage %q", err, usage)
	}
}

func TestInsideArchive(t *testing.T) {
	t.Parallel()

	for _, tc := range []struct {
		dir  string
		want bool
	}{
		{`C:\Users\a\AppData\Local\Temp\Temp1_SkyDemonConnect-v1.0.0.zip`, true},
		{`C:\Users\a\AppData\Local\Temp\Temp1_x.ZIP\SkyDemonConnect`, true},
		{`C:\Users\a\AppData\Local\Temp\7zO4C1B2A3D`, true},
		{`C:\Users\a\AppData\Local\Temp\Rar$EXa1234.5678`, true},
		{`C:\Tools\SkyDemonConnect`, false},
		{`C:\Users\a\AppData\Local\Temp\go-build123\b001\exe`, false}, // go run
		{`/tmp/zip/skydemon`, false},
	} {
		if got := insideArchive(tc.dir); got != tc.want {
			t.Errorf("insideArchive(%q) = %v", tc.dir, got)
		}
	}
}

// flags returns the config for args, as parseFlags makes it.
func flags(t *testing.T, args ...string) *config {
	t.Helper()

	c, usage, err := parseFlags(args)
	if err != nil {
		t.Fatalf("%v: %v\n%s", args, err, usage)
	}

	return &c
}

func TestPassOn(t *testing.T) {
	t.Parallel()

	c := flags(t, "-auto", "-console", "-demo", "-udp", "auto,192.168.1.23:49002",
		"-name", `My "Sim"`, "-com=COM11")

	want := []string{`-com=COM11`, `"-name=My \"Sim\""`, `-udp=auto,192.168.1.23:49002`}
	if !slices.Equal(c.passOn, want) {
		t.Errorf("passOn %q, want %q", c.passOn, want)
	}

	if c := flags(t); c.passOn != nil {
		t.Errorf("defaults: passOn %q", c.passOn)
	}
}

func TestQuoteArg(t *testing.T) {
	t.Parallel()

	for arg, want := range map[string]string{
		`-udp=auto`:   `-udp=auto`,
		``:            `""`,
		`-name=a b`:   `"-name=a b"`,
		`-name=a"b`:   `"-name=a\"b"`,
		`-name=a\ b\`: `"-name=a\ b\\"`,
	} {
		if got := quoteArg(arg); got != want {
			t.Errorf("quoteArg(%q) = %q, want %q", arg, got, want)
		}
	}
}

func TestOpenOutputs(t *testing.T) {
	t.Parallel()

	outs, err := openOutputs(t.Context(), flags(t, "-udp", "auto"), testutil.Log(t))
	if err != nil {
		t.Fatal(err)
	}
	defer outs.Close()

	if len(outs.targets) == 0 || outs.UDP == nil || outs.Serial != nil {
		t.Errorf("targets %v, udp %v, serial %v", outs.targets, outs.UDP, outs.Serial)
	}
}

func TestStartDemoEndToEnd(t *testing.T) {
	t.Parallel()

	conn := testutil.ListenUDP(t)
	c := flags(t, "-udp", conn.LocalAddr().String(), "-name", "T", "-demo",
		"-demo-lat", "51", "-demo-lon", "12")

	r, err := startFeed(t.Context(), c, input{sim: simconnect.Run},
		testutil.Log(t))
	if err != nil {
		t.Fatal(err)
	}

	err = conn.SetReadDeadline(time.Now().Add(2 * time.Second))
	if err != nil {
		t.Fatal(err)
	}

	buf := make([]byte, 512)

	n, _, err := conn.ReadFrom(buf)
	if err != nil || !strings.HasPrefix(string(buf[:n]), "XGPST,12.") {
		t.Errorf("first packet %q, %v", buf[:n], err)
	}

	start := time.Now()

	r.stop()

	if d := time.Since(start); d >= shutdownWait {
		t.Errorf("stop took %v", d)
	}
}

var errSim = errors.New("no sim")

func failingSim(_ context.Context, _ *log.Logger, h simconnect.Handlers) { h.Failed(errSim) }

func TestRunInputSimError(t *testing.T) {
	t.Parallel()

	f := feed.New(feed.Outputs{}, testutil.Log(t))
	runInput(t.Context(), f, &config{}, input{sim: failingSim},
		func() { t.Error("quit") }, testutil.Log(t))

	if st := f.Status(); st.Kind != feed.Failed || st.Err != errSim.Error() {
		t.Errorf("status %+v", st)
	}
}

func TestEndedQuits(t *testing.T) {
	t.Parallel()

	for _, tc := range []struct{ auto, watched, want bool }{
		{false, false, false},
		{true, false, true},
		{true, true, false}, // MSFS's own exit ends the app
	} {
		if got := endedQuits(tc.auto, tc.watched); got != tc.want {
			t.Errorf("endedQuits(auto %v, watched %v) = %v", tc.auto, tc.watched, got)
		}
	}
}

func TestStartFeedQuit(t *testing.T) {
	t.Parallel()

	sim := func(ctx context.Context, _ *log.Logger, h simconnect.Handlers) {
		h.Ended()
		<-ctx.Done()
	}

	conn := testutil.ListenUDP(t)

	for _, quits := range []bool{false, true} {
		synctest.Test(t, func(t *testing.T) {
			c := flags(t, "-udp", conn.LocalAddr().String())

			r, err := startFeed(t.Context(), c, input{sim: sim, endedQuits: quits}, testutil.Log(t))
			if err != nil {
				t.Fatal(err)
			}
			defer r.stop()

			synctest.Wait()

			select {
			case <-r.quit:
				if !quits {
					t.Error("quit when the session ended, without endedQuits")
				}
			default:
				if quits {
					t.Error("no quit when the session ended, with endedQuits")
				}
			}
		})
	}
}

func TestRefreshTargets(t *testing.T) {
	t.Parallel()

	synctest.Test(t, func(t *testing.T) {
		a := netip.MustParseAddrPort("127.0.0.1:1")
		b := netip.MustParseAddrPort("127.0.0.1:2")
		calls := 0
		resolve := func(context.Context) targets.List {
			calls++
			if calls < 3 {
				return targets.List{a}
			}

			return targets.List{a, b}
		}

		var sets [][]netip.AddrPort

		set := func(l []netip.AddrPort) { sets = append(sets, l) }

		ctx, cancel := context.WithCancel(t.Context())
		done := make(chan struct{})

		go func() {
			defer close(done)

			refreshTargets(ctx, targetRefresh, resolve, set, []netip.AddrPort{a}, testutil.Log(t))
		}()

		time.Sleep(5*targetRefresh + time.Second)
		synctest.Wait()
		cancel()
		<-done

		if calls != 5 || len(sets) != 1 || len(sets[0]) != 2 {
			t.Errorf("%d resolves, sets %v: want 5 and one change to [a b]", calls, sets)
		}
	})
}

func TestStatusLine(t *testing.T) {
	t.Parallel()

	var out bytes.Buffer

	s := &statusLine{out: &out}
	s.show("status")

	_, err := s.Write([]byte("log\n"))
	if err != nil {
		t.Fatal(err)
	}

	s.end() // nothing shown: no extra newline

	if got := out.String(); !strings.HasSuffix(got, clearLine()+"log\n") ||
		!strings.HasPrefix(got, "\rstatus") {
		t.Errorf("output %q", got)
	}
}

func TestRunConsole(t *testing.T) {
	t.Parallel()

	conn := testutil.ListenUDP(t)

	synctest.Test(t, func(t *testing.T) {
		var out testutil.Buffer

		ctx, cancel := context.WithCancel(t.Context())
		done := make(chan error)
		c := flags(t, "-udp", conn.LocalAddr().String(), "-demo", "-demo-lat", "51",
			"-demo-lon", "12")

		go func() { done <- runConsole(ctx, c, &out, simconnect.Run, statusEvery) }()

		time.Sleep(2 * statusEvery)
		synctest.Wait()
		cancel()

		err := <-done
		if err != nil {
			t.Errorf("runConsole: %v", err)
		}

		if got := out.String(); !strings.Contains(got, "demo: circling") ||
			!strings.Contains(got, "\r30 Hz · ") {
			t.Errorf("output %q", got)
		}
	})
}
