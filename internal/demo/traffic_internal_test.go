package demo

import (
	"math"
	"testing"
	"time"

	"github.com/saschagrunert/skydemon-connect/internal/position"
)

func TestTestTraffic(t *testing.T) {
	t.Parallel()

	start := time.Now()
	own := position.Sample{Lat: 51.4, Lon: 12.2, AltM: 762, GSKnots: 100, TrackTrue: 90}
	gen := NewTestTraffic(start)

	for _, d := range []time.Duration{0, 30 * time.Second, time.Minute, 5 * time.Minute} {
		targets := gen.At(own, start.Add(d))
		if len(targets) != 3 {
			t.Fatalf("%v: %d targets", d, len(targets))
		}

		if r := flatNM(own.Lat, own.Lon, targets[0].Lat, targets[0].Lon); math.Abs(r-2) > 0.01 {
			t.Errorf("%v: TEST1 %.3f NM away, want 2", d, r)
		}

		if !targets[0].Airborne || !targets[1].Airborne || targets[2].Airborne {
			t.Errorf("%v: airborne flags %v %v %v",
				d, targets[0].Airborne, targets[1].Airborne, targets[2].Airborne)
		}

		seen := map[uint32]bool{}
		for _, x := range targets {
			seen[x.ICAO] = true
		}

		if len(seen) != 3 {
			t.Errorf("%v: ICAO addresses not unique", d)
		}
	}

	// TEST2 starts 3 NM ahead (east), passes abeam after a minute 0.3 NM
	// to the right (south) and 300 ft above, heading the other way at
	// 180 kt relative: 80 kt west over the ground.
	start2 := gen.At(own, start)[1]
	if start2.Lon <= own.Lon || math.Abs(start2.TrackTrue-270) > 1e-9 ||
		math.Abs(start2.GSKnots-80) > 1e-9 {
		t.Errorf("TEST2 at start: %+v", start2)
	}

	abeam := gen.At(own, start.Add(time.Minute))[1]
	if r := flatNM(own.Lat, own.Lon, abeam.Lat, abeam.Lon); math.Abs(r-0.3) > 0.01 ||
		abeam.Lat >= own.Lat {
		t.Errorf("TEST2 abeam: %.3f NM, %+v", r, abeam)
	}

	if dh := (abeam.AltM - own.AltM) / position.FeetToMeters; math.Abs(dh-300) > 0.01 {
		t.Errorf("TEST2 %.1f ft above, want 300", dh)
	}
}

// Each target's track and ground speed are how it moves while the own
// aircraft flies on, also faster than TEST2 closes: an EFB that moves
// targets on between updates puts them where the next update does.
func TestTestTrafficMotion(t *testing.T) {
	t.Parallel()

	// As often as traffic is sent.
	const step = time.Second

	for _, ownKt := range []float64{0, 100, 250} {
		start := time.Now()
		gen := NewTestTraffic(start)
		own := position.Sample{Lat: 51.4, Lon: 12.2, AltM: 762, GSKnots: ownKt, TrackTrue: 40}

		before := gen.At(own, start.Add(20*time.Second))

		own.Lat, own.Lon = offset(own.Lat, own.Lon, own.TrackTrue, ownKt*step.Hours())
		after := gen.At(own, start.Add(20*time.Second+step))

		for i, b := range before {
			a := after[i]
			moved := flatNM(b.Lat, b.Lon, a.Lat, a.Lon)

			// Within 2 kt: the circle turns TEST1's velocity by 1° a
			// second.
			if kt := moved / step.Hours(); math.Abs(kt-b.GSKnots) > 2 {
				t.Errorf("own %v kt, %s: moves at %.1f kt, reports %.1f kt",
					ownKt, b.Callsign, kt, b.GSKnots)
			}

			if moved < 1e-4 {
				continue
			}

			dLat := (a.Lat - b.Lat) * nmPerDegree
			dLon := (a.Lon - b.Lon) * nmPerDegree * math.Cos(b.Lat/position.DegPerRad)
			track := position.Norm360(math.Atan2(dLon, dLat) * position.DegPerRad)

			if diff := math.Abs(position.Norm180(track - b.TrackTrue)); diff > 1 {
				t.Errorf("own %v kt, %s: moved towards %.1f°, reports %.1f°",
					ownKt, b.Callsign, track, b.TrackTrue)
			}
		}
	}
}

// TEST3 stays where it was put, and moves along once the own aircraft is
// more than 10 NM away.
func TestTestTrafficAnchor(t *testing.T) {
	t.Parallel()

	start := time.Now()
	gen := NewTestTraffic(start)
	own := position.Sample{Lat: 51.4, Lon: 12.2, AltM: 1000}

	first := gen.At(own, start)[2]

	own.Lat += 5 / nmPerDegree
	own.AltM = 2000

	if got := gen.At(own, start.Add(time.Minute))[2]; got != first {
		t.Errorf("5 NM on: TEST3 %+v, want it to stay at %+v", got, first)
	}

	own.Lat += 10 / nmPerDegree
	if got := gen.At(own, start.Add(2*time.Minute))[2]; got.Lat <= own.Lat || got.GSKnots != 0 {
		t.Errorf("15 NM on: TEST3 %+v, want 1 NM north of %v", got, own.Lat)
	}
}
