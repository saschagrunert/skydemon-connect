package nmea

import (
	"strings"
	"testing"
	"time"

	"github.com/saschagrunert/skydemon-connect/internal/position"
)

func TestCoordinate(t *testing.T) {
	t.Parallel()

	for _, tc := range []struct {
		deg      float64
		width    int
		pos, neg string
		want     string
	}{
		{51.4239, 2, "N", "S", "5125.43400,N"},
		{-33.5, 2, "N", "S", "3330.00000,S"},
		{12.2364, 3, "E", "W", "01214.18400,E"},
		{-0.5, 3, "E", "W", "00030.00000,W"},
		{50.9999999999, 2, "N", "S", "5100.00000,N"},
		{179.9999999, 3, "E", "W", "17959.99999,E"},
		{179.99999999, 3, "E", "W", "18000.00000,E"},
		{-179.99999999, 3, "E", "W", "18000.00000,W"},
		{89.9999999, 2, "N", "S", "8959.99999,N"},
		{-0.0000001, 2, "N", "S", "0000.00001,S"},
		{-0.000000001, 2, "N", "S", "0000.00000,N"},
		{0, 3, "E", "W", "00000.00000,E"},
	} {
		if got := coordinate(tc.deg, tc.width, tc.pos, tc.neg); got != tc.want {
			t.Errorf("coordinate(%v, %d) = %q, want %q", tc.deg, tc.width, got, tc.want)
		}
	}
}

func TestChecksum(t *testing.T) {
	t.Parallel()

	// The well-known example sentence from the NMEA documentation.
	if got := checksum(
		"GPGGA,123519,4807.038,N,01131.000,E,1,08,0.9,545.4,M,46.9,M,,",
	); got != "47" {
		t.Errorf("checksum = %s, want 47", got)
	}
}

func TestSentences(t *testing.T) {
	t.Parallel()

	ts := time.Date(2026, 11, 26, 14, 5, 9, 730_000_000, time.UTC)
	s := position.Sample{Lat: 51.4239, Lon: 12.2364, AltM: 762, GSKnots: 100, TrackTrue: 90}

	lines := strings.Split(strings.TrimSuffix(Sentences(s, ts), "\r\n"), "\r\n")
	if len(lines) != 3 {
		t.Fatalf("want 3 sentences, got %d: %q", len(lines), lines)
	}

	for _, l := range lines {
		star := strings.LastIndex(l, "*")
		if l[0] != '$' || star < 0 || checksum(l[1:star]) != l[star+1:] {
			t.Errorf("bad sentence %q", l)
		}
	}

	wantPrefixes := []string{
		"$GPRMC,140509.73,A,5125.43400,N,01214.18400,E,100.0,90.0,261126,,,A*",
		"$GPGGA,140509.73,5125.43400,N,01214.18400,E,1,10,0.8,762.0,M,",
		"$GPGSA,A,3,",
	}
	for i, p := range wantPrefixes {
		if !strings.HasPrefix(lines[i], p) {
			t.Errorf("sentence %d = %q, want prefix %q", i, lines[i], p)
		}
	}
}

func TestSentencesTrackWraps(t *testing.T) {
	t.Parallel()

	ts := time.Date(2026, 11, 26, 14, 5, 9, 0, time.UTC)
	out := Sentences(position.Sample{Lat: 51, Lon: 12, GSKnots: 100, TrackTrue: 359.96}, ts)

	if !strings.Contains(out, ",100.0,0.0,261126,") {
		t.Errorf("track 359.96 should print as 0.0: %q", out)
	}
}

func TestSentencesUseUTC(t *testing.T) {
	t.Parallel()

	local := time.Date(2026, 11, 27, 0, 30, 0, 0, time.FixedZone("CET", 3600))
	out := Sentences(position.Sample{Lat: 1, Lon: 1}, local)

	if !strings.HasPrefix(out, "$GPRMC,233000.00,") || !strings.Contains(out, ",261126,") {
		t.Errorf("time not converted to UTC: %q", out)
	}
}

// RMC speed over ground is unsigned, and nothing prints as "-0.0".
func TestSentencesNoNegatives(t *testing.T) {
	t.Parallel()

	ts := time.Date(2026, 11, 27, 9, 0, 0, 0, time.UTC)
	out := Sentences(position.Sample{Lat: 51, Lon: 12, AltM: -0.01, GSKnots: -0.04}, ts)

	if strings.Contains(out, "-") {
		t.Errorf("a minus sign in %q", out)
	}
}
