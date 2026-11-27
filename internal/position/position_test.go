package position_test

import (
	"math"
	"testing"
	"time"

	"github.com/saschagrunert/skydemon-connect/internal/position"
)

func TestNorm360(t *testing.T) {
	t.Parallel()

	// A slice, not a map: 0 and -0 are the same map key, so one of them
	// would be dropped.
	for _, tc := range []struct{ in, want float64 }{
		{0, 0},
		{math.Copysign(0, -1), 0},
		{360, 0},
		{370, 10},
		{-10, 350},
		{-730, 350},
		{359.5, 359.5},
		{-1e-15, 0},
	} {
		got := position.Norm360(tc.in)
		if got != tc.want || math.Signbit(got) || got >= 360 {
			t.Errorf("Norm360(%v) = %v, want %v", tc.in, got, tc.want)
		}
	}
}

func TestNorm180(t *testing.T) {
	t.Parallel()

	for _, tc := range []struct{ in, want float64 }{
		{0, 0},
		{10, 10},
		{190, -170},
		{-190, 170},
		{180, -180},
		{-359.5, 0.5},
	} {
		if got := position.Norm180(tc.in); math.Abs(got-tc.want) > 1e-9 {
			t.Errorf("Norm180(%v) = %v, want %v", tc.in, got, tc.want)
		}
	}
}

func TestRound(t *testing.T) {
	t.Parallel()

	tests := []struct {
		in       float64
		decimals int
		want     float64
	}{
		{in: 1.25, decimals: 1, want: 1.3},
		{in: -0.04, decimals: 1, want: 0},
		{in: -0.06, decimals: 1, want: -0.1},
		{in: 12.5, decimals: 0, want: 13},
	}
	for _, tc := range tests {
		got := position.Round(tc.in, tc.decimals)
		if got != tc.want || (got == 0 && math.Signbit(got)) {
			t.Errorf("Round(%v, %d) = %v, want %v", tc.in, tc.decimals, got, tc.want)
		}
	}
}

func TestRoundAngle(t *testing.T) {
	t.Parallel()

	tests := []struct {
		in       float64
		decimals int
		want     float64
	}{
		{in: 359.996, decimals: 2, want: 0},
		{in: 359.994, decimals: 2, want: 359.99},
		{in: 359.6, decimals: 0, want: 0},
		{in: -0.01, decimals: 1, want: 0},
		{in: -0.06, decimals: 1, want: 359.9},
		{in: 725, decimals: 1, want: 5},
	}
	for _, tc := range tests {
		got := position.RoundAngle(tc.in, tc.decimals)
		if got != tc.want || math.Signbit(got) {
			t.Errorf("RoundAngle(%v, %d) = %v, want %v", tc.in, tc.decimals, got, tc.want)
		}
	}
}

func TestIsNullIsland(t *testing.T) {
	t.Parallel()

	tests := []struct {
		s    position.Sample
		want bool
	}{
		{s: position.Sample{}, want: true},
		{s: position.Sample{Lat: 0.00001, Lon: -0.00001}, want: true},
		{s: position.Sample{Lat: 51.4, Lon: 0}, want: false},
		{s: position.Sample{Lat: 0, Lon: 12.2}, want: false},
	}
	for _, tc := range tests {
		if got := tc.s.IsNullIsland(); got != tc.want {
			t.Errorf("%+v: got %v, want %v", tc.s, got, tc.want)
		}
	}
}

func TestIsFinite(t *testing.T) {
	t.Parallel()

	ok := position.Sample{Lat: 51, Lon: 12, AltM: 762, GSKnots: 100}
	if !ok.IsFinite() {
		t.Errorf("%+v is finite", ok)
	}

	for _, bad := range []position.Sample{
		{Lat: math.NaN(), Lon: 12},
		{Lat: 51, Lon: math.Inf(1)},
		{Lat: 51, Lon: 12, AltM: math.Inf(-1)},
		{Lat: 51, Lon: 12, Roll: math.NaN()},
	} {
		if bad.IsFinite() {
			t.Errorf("%+v is not finite", bad)
		}
	}
}

func TestAhead(t *testing.T) {
	t.Parallel()

	const second = time.Second

	// 100 kt for a second is 51.4 m: north is latitude, east longitude
	// shrunk by the cosine of 60°.
	ahead := func(s position.Sample, d time.Duration) position.Sample { return s.Ahead(d) }

	north := ahead(position.Sample{Lat: 60, Lon: 10, GSKnots: 100, TrackTrue: 0}, second)
	if dLat := (north.Lat - 60) * 111195; math.Abs(dLat-51.44) > 0.05 || north.Lon != 10 {
		t.Errorf("north: %+v, %.2f m", north, dLat)
	}

	east := ahead(position.Sample{Lat: 60, Lon: 10, GSKnots: 100, TrackTrue: 90}, second)
	if dLon := (east.Lon - 10) * 111195 * 0.5; math.Abs(dLon-51.44) > 0.05 ||
		math.Abs(east.Lat-60) > 1e-9 {
		t.Errorf("east: %+v, %.2f m", east, dLon)
	}

	across := ahead(position.Sample{Lat: 0.5, Lon: 179.99999, GSKnots: 500, TrackTrue: 90}, second)
	if across.Lon > -179.99 || across.Lon < -180 {
		t.Errorf("across the antimeridian: %+v", across)
	}

	for name, s := range map[string]position.Sample{
		"standing":    {Lat: 51, Lon: 12, TrackTrue: 90},
		"at a pole":   {Lat: 90, Lon: 12, GSKnots: 100},
		"past a pole": {Lat: 89.99999, Lon: 12, GSKnots: 500},
	} {
		if got := s.Ahead(second); got != s {
			t.Errorf("%s: moved to %+v", name, got)
		}
	}

	s := position.Sample{Lat: 51, Lon: 12, GSKnots: 100}
	if got := s.Ahead(-second); got != s {
		t.Errorf("back in time: moved to %+v", got)
	}
}
