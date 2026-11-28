package flightplan_test

import (
	"math"
	"os"
	"strings"
	"testing"

	"github.com/saschagrunert/skydemon-connect/internal/flightplan"
)

func parseFile(t *testing.T, name string) flightplan.Route {
	t.Helper()

	f, err := os.Open("testdata/" + name)
	if err != nil {
		t.Fatal(err)
	}

	defer func() { _ = f.Close() }()

	r, err := flightplan.Parse(f)
	if err != nil {
		t.Fatal(err)
	}

	return r
}

func near(a, b float64) bool { return math.Abs(a-b) < 1e-6 }

func TestParse(t *testing.T) {
	t.Parallel()

	r := parseFile(t, "LeipzigHalle - Holzdorf.flightplan")

	want := []flightplan.Point{
		{Lat: 51 + 25.0/60 + 26.35/3600, Lon: 12 + 14.0/60 + 11.0/3600, Aerodrome: true}, // EDDP
		{Lat: 51 + 32.0/60 + 36.0/3600, Lon: 12 + 11.0/60 + 36.0/3600, Aerodrome: false}, // N1
		{Lat: 51 + 49.0/60 + 56.0/3600, Lon: 12 + 11.0/60 + 9.0/3600, Aerodrome: true},   // EDAD
		{Lat: 52 + 12.0/60 + 14.0/3600, Lon: 13 + 9.0/60 + 36.0/3600, Aerodrome: true},   // EDAZ
		{Lat: 51 + 46.0/60 + 4.0/3600, Lon: 13 + 10.0/60 + 3.0/3600, Aerodrome: true},    // ETSH
	}

	if len(r.Points) != len(want) || r.LevelFt != 2000 || !r.VFR {
		t.Fatalf("got %+v", r)
	}

	for i, p := range r.Points {
		w := want[i]
		if !near(p.Lat, w.Lat) || !near(p.Lon, w.Lon) || p.Aerodrome != w.Aerodrome {
			t.Errorf("point %d: %+v, want %+v", i, p, want[i])
		}
	}
}

// As saved on the PC: CRLF, the aircraft only referenced, no points between.
func TestParseDirect(t *testing.T) {
	t.Parallel()

	b, err := os.ReadFile("testdata/Padova - TrevisoS.Angelo.flightplan")
	if err != nil {
		t.Fatal(err)
	}

	r, err := flightplan.Parse(strings.NewReader(strings.ReplaceAll(string(b), "\n", "\r\n")))
	if err != nil {
		t.Fatal(err)
	}

	if len(r.Points) != 2 || !r.Points[0].Aerodrome || !r.Points[1].Aerodrome {
		t.Fatalf("got %+v", r)
	}

	if !near(r.Points[1].Lat, 45+39.0/60+3.0/3600) || !near(r.Points[1].Lon, 12+11.0/60+52.0/3600) {
		t.Errorf("LIPH at %+v", r.Points[1])
	}
}

func TestParseSouthWestAndLevels(t *testing.T) {
	t.Parallel()

	for _, tc := range []struct {
		level string
		want  int
	}{{"FL55", 5500}, {"fl 75", 0}, {"MSL", 0}, {"", 0}, {"-100", 0}, {"3500", 3500}} {
		in := `<DivelementsFlightPlanner><PrimaryRoute Start="S334649.50 W0701442.00" StartType="Aerodrome"` +
			` Level="` + tc.level + `" Rules="Ifr"><GreatCircleRoute To="S334600.00 W0701400.00" ToType="Waypoint" />` +
			`</PrimaryRoute></DivelementsFlightPlanner>`

		r, err := flightplan.Parse(strings.NewReader(in))
		if err != nil {
			t.Fatal(err)
		}

		if r.LevelFt != tc.want || r.VFR {
			t.Errorf("level %q: %+v", tc.level, r)
		}

		p := r.Points[0]
		if !near(p.Lat, -(33+46.0/60+49.5/3600)) || !near(p.Lon, -(70+14.0/60+42.0/3600)) {
			t.Errorf("start %+v", p)
		}

		if r.Points[1].Aerodrome {
			t.Error("a waypoint as aerodrome")
		}
	}
}

func TestParseErrors(t *testing.T) {
	t.Parallel()

	route := func(start, to string) string {
		return `<DivelementsFlightPlanner><PrimaryRoute Start="` + start + `" StartType="Aerodrome">` +
			`<RhumbLineRoute To="` + to + `" ToType="Aerodrome" /></PrimaryRoute></DivelementsFlightPlanner>`
	}

	good := "N512526.35 E0121411.00"

	for name, doc := range map[string]string{
		"not XML":         "{}",
		"no route":        "<DivelementsFlightPlanner/>",
		"one point":       `<DivelementsFlightPlanner><PrimaryRoute Start="` + good + `"/></DivelementsFlightPlanner>`,
		"no start":        route("", good),
		"bad hemisphere":  route("X512526.35 E0121411.00", good),
		"lat as lon":      route("E512526.35 N0121411.00", good),
		"minutes over 59": route(good, "N516026.35 E0121411.00"),
		"seconds 60":      route(good, "N512560.00 E0121411.00"),
		"over the pole":   route(good, "N910000.00 E0121411.00"),
		"lon over 180":    route(good, "N512526.35 E1810000.00"),
		"too short":       route(good, "N5125 E01214"),
		"one part":        route(good, "N512526.35"),
		"NaN seconds":     route(good, "N5125NaN E0121411.00"),
		"signed degrees":  route(good, "N-10000.00 E0121411.00"),
		"signed minutes":  route(good, "N51+526.35 E0121411.00"),
	} {
		_, err := flightplan.Parse(strings.NewReader(doc))
		if err == nil {
			t.Errorf("%s: parsed", name)
		}
	}
}
