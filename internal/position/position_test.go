package position_test

import (
	"math"
	"testing"

	"github.com/saschagrunert/skydemon-connect/internal/position"
)

func TestNorm360(t *testing.T) {
	t.Parallel()

	for in, want := range map[float64]float64{
		0: 0, 360: 0, 370: 10, -10: 350, -730: 350, 359.5: 359.5, -1e-15: 0, math.Copysign(0, -1): 0,
	} {
		got := position.Norm360(in)
		if got != want || math.Signbit(got) || got >= 360 {
			t.Errorf("Norm360(%v) = %v, want %v", in, got, want)
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

func TestTrackFromVelocity(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name                       string
		east, north, gs, hdg, want float64
	}{
		{name: "north", east: 0, north: 100, gs: 60, hdg: 5, want: 0},
		{name: "east", east: 100, north: 0, gs: 60, hdg: 5, want: 90},
		{name: "south", east: 0, north: -100, gs: 60, hdg: 5, want: 180},
		{name: "west", east: -100, north: 0, gs: 60, hdg: 5, want: 270},
		{name: "slow uses heading", east: -100, north: 0, gs: 1, hdg: 123, want: 123},
		{name: "slow heading normalised", east: 0, north: 0, gs: 0, hdg: -10, want: 350},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			if got := position.TrackFromVelocity(tc.east, tc.north, tc.gs, tc.hdg); got != tc.want {
				t.Errorf("got %v, want %v", got, tc.want)
			}
		})
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
