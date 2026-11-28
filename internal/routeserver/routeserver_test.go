package routeserver_test

import (
	"context"
	"encoding/json"
	"errors"
	"log"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/saschagrunert/skydemon-connect/internal/routeserver"
	"github.com/saschagrunert/skydemon-connect/internal/testutil"
)

const (
	coui   = "coui://html_ui"
	plugin = "0123456789abcdef"
)

func copyRoute(t *testing.T, dir, name string, mod time.Time) string {
	t.Helper()

	b, err := os.ReadFile(filepath.Join("..", "flightplan", "testdata", name))
	if err != nil {
		t.Fatal(err)
	}

	path := filepath.Join(dir, name)

	err = os.WriteFile(path, b, 0o600)
	if err != nil {
		t.Fatal(err)
	}

	err = os.Chtimes(path, mod, mod)
	if err != nil {
		t.Fatal(err)
	}

	return path
}

func get(t *testing.T, s http.Handler, host, path, origin string) *httptest.ResponseRecorder {
	t.Helper()

	req := httptest.NewRequestWithContext(t.Context(), http.MethodGet, "http://"+host+path, nil)
	req.Host = host

	if origin != "" {
		req.Header.Set("Origin", origin)
	}

	rec := httptest.NewRecorder()
	s.ServeHTTP(rec, req)

	return rec
}

func decode(t *testing.T, rec *httptest.ResponseRecorder) routeserver.Route {
	t.Helper()

	if rec.Code != http.StatusOK {
		t.Fatalf("status %d: %s", rec.Code, rec.Body)
	}

	var r routeserver.Route

	err := json.Unmarshal(rec.Body.Bytes(), &r)
	if err != nil {
		t.Fatal(err)
	}

	return r
}

func TestNewestRoute(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	now := time.Now()

	copyRoute(t, dir, "LeipzigHalle - Holzdorf.flightplan", now.Add(-time.Hour))
	newer := copyRoute(t, dir, "Padova - TrevisoS.Angelo.flightplan", now)

	var out testutil.Buffer

	s := routeserver.New(dir, plugin, log.New(&out, "", 0))

	rec := get(t, s, routeserver.Addr, "/route", coui)
	r := decode(t, rec)

	if r.Name != "Padova - TrevisoS.Angelo" || len(r.Points) != 2 || !r.Points[0].Aerodrome ||
		r.LevelFt != 2000 || !r.VFR || r.ID == "" {
		t.Errorf("route %+v", r)
	}

	if rec.Header().Get("Access-Control-Allow-Origin") != coui ||
		rec.Header().Get("Cache-Control") != "no-store" ||
		rec.Header().Get(routeserver.PluginHeader) != plugin ||
		rec.Header().Get("Access-Control-Expose-Headers") != routeserver.PluginHeader {
		t.Errorf("headers %v", rec.Header())
	}

	// The same file again: the same ID, logged once.
	if again := decode(t, get(t, s, routeserver.Addr, "/route", coui)); again.ID != r.ID {
		t.Errorf("ID changed to %q", again.ID)
	}

	if n := strings.Count(out.String(), "for the G1000"); n != 1 {
		t.Errorf("logged %d times:\n%s", n, out.String())
	}

	// Saved again with changes: a new ID.
	b, err := os.ReadFile(newer)
	if err != nil {
		t.Fatal(err)
	}

	edited := strings.Replace(string(b), `Level="2000"`, `Level="2500"`, 1)

	err = os.WriteFile(newer, []byte(edited), 0o600)
	if err != nil {
		t.Fatal(err)
	}

	err = os.Chtimes(newer, now.Add(time.Second), now.Add(time.Second))
	if err != nil {
		t.Fatal(err)
	}

	changed := decode(t, get(t, s, routeserver.Addr, "/route", coui))
	if changed.ID == r.ID || changed.LevelFt != 2500 {
		t.Errorf("after saving again %+v", changed)
	}
}

// A G1000 that runs another plugin than the one installed, as after an
// update until MSFS restarts, is logged once; the plugin learns of it from
// the header, also without a route.
func TestOtherPlugin(t *testing.T) {
	t.Parallel()

	var out testutil.Buffer

	s := routeserver.New(t.TempDir(), plugin, log.New(&out, "", 0))

	for _, path := range []string{"/route?plugin=" + plugin, "/route?plugin=old", "/route?plugin=old"} {
		rec := get(t, s, routeserver.Addr, path, coui)
		if rec.Code != http.StatusNotFound || rec.Header().Get(routeserver.PluginHeader) != plugin {
			t.Errorf("%s: status %d, headers %v", path, rec.Code, rec.Header())
		}
	}

	if n := strings.Count(out.String(), "another plugin"); n != 1 ||
		!strings.Contains(out.String(), `asked for the first time, from origin "coui://html_ui"`) {
		t.Errorf("logged %d times:\n%s", n, out.String())
	}
}

// Requests without the plugin's ID, e.g. from curl, aren't the G1000's.
func TestNotFromPlugin(t *testing.T) {
	t.Parallel()

	var out testutil.Buffer

	s := routeserver.New(t.TempDir(), plugin, log.New(&out, "", 0))
	get(t, s, routeserver.Addr, "/route", "")

	if strings.Contains(out.String(), "G1000") {
		t.Errorf("log:\n%s", out.String())
	}
}

// Routes in SkyDemon's folders below Routes count, down to two levels.
func TestRouteInFolder(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	now := time.Now()

	copyRoute(t, dir, "LeipzigHalle - Holzdorf.flightplan", now.Add(-time.Hour))

	deep := filepath.Join(dir, "Italy", "Veneto", "Too deep")

	err := os.MkdirAll(deep, 0o700)
	if err != nil {
		t.Fatal(err)
	}

	copyRoute(t, deep, "Padova - TrevisoS.Angelo.flightplan", now)

	s := routeserver.New(dir, plugin, testutil.Log(t))

	r := decode(t, get(t, s, routeserver.Addr, "/route", coui))
	if r.Name != "LeipzigHalle - Holzdorf" {
		t.Errorf("too deep a route served: %+v", r)
	}

	copyRoute(t, filepath.Dir(deep), "Padova - TrevisoS.Angelo.flightplan", now)

	r = decode(t, get(t, s, routeserver.Addr, "/route", coui))
	if r.Name != "Padova - TrevisoS.Angelo" {
		t.Errorf("route in a folder not served: %+v", r)
	}
}

func TestNoRoute(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()

	var out testutil.Buffer

	s := routeserver.New(dir, plugin, log.New(&out, "", 0))

	for range 2 {
		if rec := get(t, s, routeserver.Addr, "/route", coui); rec.Code != http.StatusNotFound {
			t.Errorf("status %d", rec.Code)
		}
	}

	if n := strings.Count(out.String(), "no saved SkyDemon route yet"); n != 1 {
		t.Errorf("logged %d times:\n%s", n, out.String())
	}

	// Not a route, a folder and a broken one: still none, the broken one
	// logged.
	for _, name := range []string{"notes.txt", "old.gpx"} {
		err := os.WriteFile(filepath.Join(dir, name), []byte("x"), 0o600)
		if err != nil {
			t.Fatal(err)
		}
	}

	err := os.Mkdir(filepath.Join(dir, "folder.flightplan"), 0o700)
	if err != nil {
		t.Fatal(err)
	}

	err = os.WriteFile(filepath.Join(dir, "broken.FLIGHTPLAN"), []byte("<x/>"), 0o600)
	if err != nil {
		t.Fatal(err)
	}

	if rec := get(t, s, routeserver.Addr, "/route", coui); rec.Code != http.StatusNotFound {
		t.Errorf("status %d", rec.Code)
	}

	if !strings.Contains(out.String(), "broken.FLIGHTPLAN: parse: no PrimaryRoute") {
		t.Errorf("log:\n%s", out.String())
	}

	missing := routeserver.New(filepath.Join(dir, "missing"), plugin, testutil.Log(t))
	if rec := get(t, missing, routeserver.Addr, "/route", coui); rec.Code != http.StatusNotFound {
		t.Errorf("missing folder: status %d", rec.Code)
	}
}

func TestTooBig(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()

	err := os.WriteFile(filepath.Join(dir, "big.flightplan"), make([]byte, 1<<20+1), 0o600)
	if err != nil {
		t.Fatal(err)
	}

	var out testutil.Buffer

	s := routeserver.New(dir, plugin, log.New(&out, "", 0))
	if rec := get(t, s, routeserver.Addr, "/route", coui); rec.Code != http.StatusNotFound ||
		!strings.Contains(out.String(), "larger than 1 MiB") {
		t.Errorf("status %d, log %q", rec.Code, out.String())
	}
}

// Web pages get nothing: not by another host name, and without the sim's
// origin, not readable.
func TestRefused(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	copyRoute(t, dir, "Padova - TrevisoS.Angelo.flightplan", time.Now())

	var out testutil.Buffer

	s := routeserver.New(dir, plugin, log.New(&out, "", 0))

	for _, host := range []string{"localhost:49080", "attacker.example:49080", "127.0.0.1"} {
		if rec := get(t, s, host, "/route", coui); rec.Code != http.StatusForbidden {
			t.Errorf("host %q: status %d", host, rec.Code)
		}
	}

	for _, origin := range []string{
		"https://attacker.example", "http://127.0.0.1:49080", "", "https://attacker.example",
	} {
		rec := get(t, s, routeserver.Addr, "/route", origin)
		if rec.Code != http.StatusOK || rec.Header().Get("Access-Control-Allow-Origin") != "" {
			t.Errorf("origin %q: status %d, headers %v", origin, rec.Code, rec.Header())
		}
	}

	// A sandboxed iframe or a file:// page, of any web page.
	if rec := get(t, s, routeserver.Addr, "/route", "null"); rec.Header().
		Get("Access-Control-Allow-Origin") != "" {
		t.Errorf("null origin: headers %v", rec.Header())
	}

	if !strings.Contains(out.String(), `refused a request from "null"`) {
		t.Errorf("log:\n%s", out.String())
	}

	if rec := get(t, s, routeserver.Addr, "/other", coui); rec.Code != http.StatusNotFound {
		t.Errorf("other path: status %d", rec.Code)
	}

	req := httptest.NewRequestWithContext(
		t.Context(),
		http.MethodPost,
		"http://"+routeserver.Addr+"/route",
		nil,
	)
	rec := httptest.NewRecorder()
	s.ServeHTTP(rec, req)

	if rec.Code != http.StatusNotFound {
		t.Errorf("POST: status %d", rec.Code)
	}
}

func TestRun(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	copyRoute(t, dir, "Padova - TrevisoS.Angelo.flightplan", time.Now())

	ctx, cancel := context.WithCancel(t.Context())
	done := make(chan error, 1)

	go func() { done <- routeserver.New(dir, plugin, testutil.Log(t)).Run(ctx) }()

	var (
		resp *http.Response
		err  error
	)

	for range 50 {
		select {
		case err := <-done:
			t.Skipf("port taken here, e.g. by SkyDemonConnect: %v", err)
		default:
		}

		url := "http://" + routeserver.Addr + "/route"
		req, _ := http.NewRequestWithContext(ctx, http.MethodGet, url, http.NoBody)

		resp, err = http.DefaultClient.Do(req)
		if err == nil {
			break
		}

		time.Sleep(20 * time.Millisecond)
	}

	if err != nil {
		t.Fatal(err)
	}

	_ = resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		// Another server answered, the port being taken.
		select {
		case err := <-done:
			t.Skipf("port taken here, e.g. by SkyDemonConnect: %v", err)
		case <-time.After(100 * time.Millisecond):
		}

		t.Errorf("status %d", resp.StatusCode)
	}

	cancel()

	// Taken between the request and the listen, e.g. by the tests of the
	// app running alongside.
	var opErr *net.OpError

	err = <-done
	if errors.As(err, &opErr) && opErr.Op == "listen" {
		t.Skipf("port taken here: %v", err)
	}

	if err != nil {
		t.Error(err)
	}
}
