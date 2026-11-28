package gtn_test

import (
	"log"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"testing/synctest"
	"time"

	"github.com/saschagrunert/skydemon-connect/internal/flightplan"
	"github.com/saschagrunert/skydemon-connect/internal/gtn"
	"github.com/saschagrunert/skydemon-connect/internal/position"
	"github.com/saschagrunert/skydemon-connect/internal/routeserver"
	"github.com/saschagrunert/skydemon-connect/internal/testutil"
)

// exported is what SkyDemon itself exports for the route as GFP.
const exported = "FPN/RI:F:EDDP,N51254E012142:F:N51326E012116:F:EDAD,N51499E012111:" +
	"F:EDAZ,N52122E013096:F:ETSH,N51461E013100"

// leipzigHolzdorf is the test route as the route server serves it.
func leipzigHolzdorf(t *testing.T) *routeserver.Route {
	t.Helper()

	f, err := os.Open("../flightplan/testdata/LeipzigHalle - Holzdorf.flightplan")
	if err != nil {
		t.Fatal(err)
	}

	defer func() { _ = f.Close() }()

	plan, err := flightplan.Parse(f)
	if err != nil {
		t.Fatal(err)
	}

	r := &routeserver.Route{
		ID:      "1",
		Name:    "LeipzigHalle - Holzdorf",
		Points:  nil,
		LevelFt: 2000,
		VFR:     true,
	}

	for _, p := range plan.Points {
		r.Points = append(r.Points,
			routeserver.Point{Lat: p.Lat, Lon: p.Lon, Aerodrome: p.Aerodrome})
	}

	return r
}

// simAirports are the sim's airports of the route, a few hundred metres
// from SkyDemon's positions, and two others nearby.
func simAirports() []position.Airport {
	return []position.Airport{
		{Ident: "EDDP", Lat: 51.4239, Lon: 12.2364},
		{Ident: "EDAD", Lat: 51.8313, Lon: 12.1861},
		{Ident: "EDAZ", Lat: 52.2040, Lon: 13.1586},
		{Ident: "ETSH", Lat: 51.7678, Lon: 13.1675},
		// Leipzig's other airfield, 25 km off: never chosen.
		{Ident: "EDAC", Lat: 50.9817, Lon: 12.5061},
		// 1.5 NM from Dessau: further than EDAD itself.
		{Ident: "XXXX", Lat: 51.8550, Lon: 12.1861},
	}
}

func TestGFP(t *testing.T) {
	t.Parallel()

	plan, idents := gtn.GFP(leipzigHolzdorf(t), simAirports())
	if plan != exported {
		t.Errorf("got\n%s\nwant\n%s", plan, exported)
	}

	if strings.Join(idents, " ") != "EDDP EDAD EDAZ ETSH" {
		t.Errorf("airports %q", idents)
	}

	// Without the airports, every point by its position.
	plan, idents = gtn.GFP(leipzigHolzdorf(t), nil)
	if strings.Contains(plan, "ED") || idents != nil {
		t.Errorf("without airports %q, %q", plan, idents)
	}

	south := &routeserver.Route{ID: "2", Name: "x", Points: []routeserver.Point{
		{Lat: -33.780417, Lon: -70.245, Aerodrome: false},
		{Lat: -0.0001, Lon: 179.99999, Aerodrome: true},
	}, LevelFt: 0, VFR: false}

	if plan, _ := gtn.GFP(south, nil); plan != "FPN/RI:F:S33468W070147:F:S00000E180000" {
		t.Errorf("south and west %q", plan)
	}
}

func TestWriter(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()

	var out testutil.Buffer

	w := gtn.New(dir, log.New(&out, "", 0))
	if w.Dir() != dir {
		t.Errorf("dir %q", w.Dir())
	}

	// Airports first: nothing to write yet.
	w.SetAirports(nil)

	path := filepath.Join(dir, gtn.Name+".gfp")

	_, err := os.Stat(path)
	if !os.IsNotExist(err) {
		t.Errorf("written without a route: %v", err)
	}

	route := leipzigHolzdorf(t)

	w.SetRoute(route)

	if b, _ := os.ReadFile(path); strings.Contains(string(b), "EDDP") {
		t.Errorf("airports before they came: %s", b)
	}

	w.SetAirports(simAirports())

	if b, _ := os.ReadFile(path); string(b) != exported {
		t.Errorf("with airports %s", b)
	}

	// The same again: not written again.
	w.SetRoute(route)

	if n := strings.Count(out.String(), "for the GTN"); n != 2 {
		t.Errorf("written %d times:\n%s", n, out.String())
	}

	if !strings.Contains(out.String(), "by positions only") ||
		!strings.Contains(out.String(), "with the airports EDDP, EDAD, EDAZ, ETSH") {
		t.Errorf("log:\n%s", out.String())
	}

	if left, _ := filepath.Glob(filepath.Join(dir, ".*")); len(left) > 0 {
		t.Errorf("left behind %v", left)
	}
}

// A link planted in the shared FPL folder under the plan's name is
// replaced, and what it points to left alone.
func TestWriterReplacesLink(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	victim := filepath.Join(t.TempDir(), "victim.txt")

	err := os.WriteFile(victim, []byte("keep"), 0o600)
	if err != nil {
		t.Fatal(err)
	}

	err = os.Symlink(victim, filepath.Join(dir, gtn.Name+".gfp"))
	if err != nil {
		t.Skipf("no symlinks here: %v", err)
	}

	gtn.New(dir, testutil.Log(t)).SetRoute(leipzigHolzdorf(t))

	if b, _ := os.ReadFile(victim); string(b) != "keep" {
		t.Errorf("link followed: %q", b)
	}

	fi, err := os.Lstat(filepath.Join(dir, gtn.Name+".gfp"))
	if err != nil || !fi.Mode().IsRegular() {
		t.Errorf("plan %v, %v", fi, err)
	}
}

func TestWriterFolderGone(t *testing.T) {
	t.Parallel()

	var out testutil.Buffer

	gtn.New(filepath.Join(t.TempDir(), "gone"), log.New(&out, "", 0)).SetRoute(leipzigHolzdorf(t))

	if !strings.Contains(out.String(), "route: GTN flight plan: GTN folder") {
		t.Errorf("log:\n%s", out.String())
	}
}

func TestFolder(t *testing.T) {
	t.Parallel()

	programData := t.TempDir()

	if got := gtn.Folder(programData); got != "" {
		t.Errorf("without the GTNXi: %q", got)
	}

	fpl := filepath.Join(programData, "TDS", "GTNXi", "FPL")

	err := os.MkdirAll(fpl, 0o700)
	if err != nil {
		t.Fatal(err)
	}

	if got := gtn.Folder(programData); got != fpl {
		t.Errorf("got %q, want %q", got, fpl)
	}

	if got := gtn.Folder(""); got != "" {
		t.Errorf("no %%PROGRAMDATA%%: %q", got)
	}
}

// A write that fails, e.g. while the GTN holds the file, is tried again,
// and logged once.
func TestWriterRetries(t *testing.T) {
	t.Parallel()

	synctest.Test(t, func(t *testing.T) {
		dir := t.TempDir()
		path := filepath.Join(dir, gtn.Name+".gfp")

		// A folder in its place: renaming the plan over it fails.
		err := os.Mkdir(path, 0o700)
		if err != nil {
			t.Fatal(err)
		}

		var out testutil.Buffer

		w := gtn.New(dir, log.New(&out, "", 0))
		w.SetRoute(leipzigHolzdorf(t))

		time.Sleep(12 * time.Second)
		synctest.Wait()

		if n := strings.Count(out.String(), "trying again"); n != 1 {
			t.Errorf("logged %d times:\n%s", n, out.String())
		}

		err = os.Remove(path)
		if err != nil {
			t.Fatal(err)
		}

		time.Sleep(6 * time.Second)
		synctest.Wait()

		b, err := os.ReadFile(path)
		if err != nil || !strings.HasPrefix(string(b), "FPN/RI:") {
			t.Errorf("not written again: %q, %v", b, err)
		}

		// Nothing pending any more: the bubble ends without a timer.
		time.Sleep(6 * time.Second)
	})
}

// Closed, a writer writes nothing and stops retrying.
func TestWriterClose(t *testing.T) {
	t.Parallel()

	synctest.Test(t, func(t *testing.T) {
		var out testutil.Buffer

		w := gtn.New(filepath.Join(t.TempDir(), "gone"), log.New(&out, "", 0))
		w.SetRoute(leipzigHolzdorf(t))
		w.Close()

		// The bubble would wait for a pending retry forever.
		time.Sleep(time.Minute)
		synctest.Wait()

		dir := t.TempDir()
		closed := gtn.New(dir, testutil.Log(t))
		closed.Close()
		closed.SetRoute(leipzigHolzdorf(t))

		_, err := os.Stat(filepath.Join(dir, gtn.Name+".gfp"))
		if !os.IsNotExist(err) {
			t.Errorf("written once closed: %v", err)
		}
	})
}

// At a start before the airports came, a plan of the same route with its
// codes is kept, not replaced by its positions.
func TestWriterKeepsCodes(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	path := filepath.Join(dir, gtn.Name+".gfp")

	err := os.WriteFile(path, []byte(exported), 0o600)
	if err != nil {
		t.Fatal(err)
	}

	w := gtn.New(dir, testutil.Log(t))
	w.SetRoute(leipzigHolzdorf(t))

	if b, _ := os.ReadFile(path); string(b) != exported {
		t.Errorf("codes lost: %s", b)
	}

	// A short list, of some of its airports, keeps the codes too; the
	// whole one writes them again.
	w.SetAirports(simAirports()[:1])

	if b, _ := os.ReadFile(path); string(b) != exported {
		t.Errorf("codes lost to a short list: %s", b)
	}

	w.SetAirports(simAirports())

	if b, _ := os.ReadFile(path); string(b) != exported {
		t.Errorf("with the whole list %s", b)
	}

	// Another route is written, with the codes there are.
	other := leipzigHolzdorf(t)
	other.ID, other.Points = "other", other.Points[:2]
	w.SetRoute(other)

	if b, _ := os.ReadFile(path); string(b) != "FPN/RI:F:EDDP,N51254E012142:F:N51326E012116" {
		t.Errorf("other route %s", b)
	}
}
