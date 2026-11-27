package foreflight_test

import (
	"math"
	"testing"

	"github.com/saschagrunert/skydemon-connect/internal/foreflight"
	"github.com/saschagrunert/skydemon-connect/internal/position"
)

func TestMessages(t *testing.T) {
	t.Parallel()

	s := position.Sample{
		Lat: 51.5, Lon: -0.25, AltM: 1000, GSKnots: 100, TrackTrue: 359.5,
		HeadingTrue: 10, Pitch: 2.5, Roll: -15,
	}

	if got, want := foreflight.XGPS(
		"MSFS",
		s,
	), "XGPSMSFS,-0.250000,51.500000,1000.0,359.50,51.44"; got != want {
		t.Errorf("XGPS = %q, want %q", got, want)
	}

	if got, want := foreflight.XATT("MSFS", s), "XATTMSFS,10.0,2.5,-15.0"; got != want {
		t.Errorf("XATT = %q, want %q", got, want)
	}
}

func TestAnglesWrapAndHaveNoNegativeZero(t *testing.T) {
	t.Parallel()

	s := position.Sample{
		Lat: 51.5, Lon: 12, TrackTrue: 359.996, HeadingTrue: -0.01, Pitch: -0.04,
		Roll: math.Copysign(0, -1),
	}

	want := "XGPST,12.000000,51.500000,0.0,0.00,0.00"
	if got := foreflight.XGPS("T", s); got != want {
		t.Errorf("XGPS = %q, want %q", got, want)
	}

	if got, want := foreflight.XATT("T", s), "XATTT,0.0,0.0,0.0"; got != want {
		t.Errorf("XATT = %q, want %q", got, want)
	}
}

// Values that round to zero print as zero, and a speed is never negative.
func TestXGPSNoNegativeZero(t *testing.T) {
	t.Parallel()

	s := position.Sample{Lat: -0.0000001, Lon: 12, AltM: -0.01, GSKnots: -0.001}

	if got, want := foreflight.XGPS("T", s), "XGPST,12.000000,0.000000,0.0,0.00,0.00"; got != want {
		t.Errorf("XGPS = %q, want %q", got, want)
	}
}
