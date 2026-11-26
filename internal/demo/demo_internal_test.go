package demo

import (
	"context"
	"math"
	"testing"
	"testing/synctest"
	"time"

	"github.com/saschagrunert/skydemon-connect/internal/position"
)

const lat, lon = 51.4239, 12.2364

func distanceNM(s *position.Sample) float64 {
	dLat := (s.Lat - lat) * nmPerDegree
	dLon := (s.Lon - lon) * nmPerDegree * math.Cos(lat*math.Pi/180)

	return math.Hypot(dLat, dLon)
}

func TestCircle(t *testing.T) {
	t.Parallel()

	start := at(lat, lon, 0)
	if start.Lat <= lat || math.Abs(start.Lon-lon) > 1e-9 || start.TrackTrue != 90 {
		t.Errorf("should start due north heading east: %+v", start)
	}

	// A full circle of 2π·3 NM at 100 kt takes about 11.3 minutes.
	for _, d := range []time.Duration{0, time.Minute, 5 * time.Minute, 11 * time.Minute} {
		s := at(lat, lon, d)
		if r := distanceNM(&s); math.Abs(r-3) > 0.01 {
			t.Errorf("%v: %.3f NM from centre, want 3", d, r)
		}

		if s.Roll <= 0 || s.GSKnots != 100 {
			t.Errorf("%v: should be a right turn at 100 kt: %+v", d, s)
		}
	}

	quarterHours := 2 * math.Pi * 3 / 100 / 4 // hours

	quarter := at(lat, lon, time.Duration(quarterHours*float64(time.Hour)))
	if math.Abs(quarter.TrackTrue-180) > 0.1 || quarter.Lon <= lon {
		t.Errorf("after a quarter circle: east of centre heading south, got %+v", quarter)
	}
}

func TestRun(t *testing.T) {
	t.Parallel()

	synctest.Test(t, func(t *testing.T) {
		ctx, cancel := context.WithTimeout(t.Context(), time.Second)
		defer cancel()

		var last position.Sample

		n := 0

		Run(ctx, lat, lon, func(s position.Sample) { n, last = n+1, s })

		// A second of simulated time at 60 Hz.
		if n != framesPerSec || math.Abs(distanceNM(&last)-3) > 0.01 {
			t.Errorf("%d updates in a second, last %+v", n, last)
		}
	})
}

func TestAntimeridian(t *testing.T) {
	t.Parallel()

	for _, d := range []time.Duration{0, 10 * time.Second, 30 * time.Second, time.Minute} {
		s := at(0, 179.99, d)
		if s.Lon < -180 || s.Lon >= 180 {
			t.Errorf("%v: longitude %v out of range", d, s.Lon)
		}
	}

	if got := wrapLon(180.07); math.Abs(got-(-179.93)) > 1e-9 {
		t.Errorf("wrapLon(180.07) = %v", got)
	}
}
