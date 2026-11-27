package demo

import (
	"math"
	"time"

	"github.com/saschagrunert/skydemon-connect/internal/position"
)

// Made-up targets around the own aircraft, to see how an EFB shows
// traffic sent this way.
const (
	circleRadiusNM = 2.0
	circleSpeedKt  = 120.0 // relative to the own aircraft

	passEveryS    = 120.0 // a pass alongside every two minutes
	passFromNM    = 3.0   // ahead to as far behind
	passOffsetNM  = 0.3   // to the right of the own track
	passAboveFt   = 300.0
	groundNorthNM = 1.0
	groundBelowFt = 2000.0
	// reanchorNM: TEST3 moves to the own aircraft once it is this far
	// away, e.g. after a flight elsewhere was loaded.
	reanchorNM = 10.0

	halfCircle = 180.0

	// ICAO addresses of the made-up targets.
	icaoTest1 = 0xF00001
	icaoTest2 = 0xF00002
	icaoTest3 = 0xF00003
)

// offset moves lat/lon dist NM towards bearing (degrees true). Flat, which
// is close enough over a few miles.
func offset(lat, lon, bearing, dist float64) (float64, float64) {
	b := bearing / position.DegPerRad

	return lat + dist/nmPerDegree*math.Cos(b),
		wrapLon(lon + dist/nmPerDegree*math.Sin(b)/math.Cos(lat/position.DegPerRad))
}

// flatNM is the flat distance between two points.
func flatNM(lat1, lon1, lat2, lon2 float64) float64 {
	return math.Hypot((lat2-lat1)*nmPerDegree,
		position.Norm180(lon2-lon1)*nmPerDegree*math.Cos(lat1/position.DegPerRad))
}

// withOwn returns the track and ground speed of a target that moves at
// relKt towards relTrack relative to own: what it really makes good, so
// that an EFB that moves targets on between updates moves it right.
func withOwn(own position.Sample, relTrack, relKt float64) (float64, float64) {
	ownRad, relRad := own.TrackTrue/position.DegPerRad, relTrack/position.DegPerRad
	east := own.GSKnots*math.Sin(ownRad) + relKt*math.Sin(relRad)
	north := own.GSKnots*math.Cos(ownRad) + relKt*math.Cos(relRad)

	return position.Norm360(math.Atan2(east, north) * position.DegPerRad), math.Hypot(east, north)
}

// TestTraffic makes three made-up aircraft around the own one:
//
//   - TEST1 circles the own aircraft anticlockwise, 2 NM out at its
//     altitude, at 120 kt relative to it.
//   - TEST2 passes head-on every two minutes, from 3 NM ahead to 3 NM
//     behind, 0.3 NM to the right and 300 ft above: close enough for a
//     traffic warning.
//   - TEST3 stands on the ground, 1 NM north of where the own aircraft
//     was first seen and 2000 ft below, until it is 10 NM away.
//
// It is not safe for concurrent use.
type TestTraffic struct {
	start time.Time
	// anchorLat, anchorLon and anchorAltM are where TEST3 stands;
	// anchored is false until then.
	anchorLat, anchorLon, anchorAltM float64
	anchored                         bool
}

// NewTestTraffic returns test traffic that starts at start.
func NewTestTraffic(start time.Time) *TestTraffic { return &TestTraffic{start: start} }

// At returns the test traffic around own at now.
func (tt *TestTraffic) At(own position.Sample, now time.Time) []position.Traffic {
	t := now.Sub(tt.start).Seconds()

	omega := circleSpeedKt / circleRadiusNM / secondsPerH // rad/s
	bearing := position.Norm360(-omega * t * position.DegPerRad)
	lat1, lon1 := offset(own.Lat, own.Lon, bearing, circleRadiusNM)
	track1, gs1 := withOwn(own, bearing-quarterCircle, circleSpeedKt)

	phase := math.Mod(t, passEveryS) / passEveryS
	along := passFromNM - 2*passFromNM*phase
	lat2, lon2 := offset(own.Lat, own.Lon, own.TrackTrue, along)
	lat2, lon2 = offset(lat2, lon2, own.TrackTrue+quarterCircle, passOffsetNM)
	// Relative to the own aircraft it moves 6 NM in two minutes, head-on.
	closing := 2 * passFromNM / passEveryS * secondsPerH
	track2, gs2 := withOwn(own, own.TrackTrue+halfCircle, closing)

	if !tt.anchored || flatNM(own.Lat, own.Lon, tt.anchorLat, tt.anchorLon) > reanchorNM {
		tt.anchorLat, tt.anchorLon = offset(own.Lat, own.Lon, 0, groundNorthNM)
		tt.anchorAltM = max(own.AltM-groundBelowFt*position.FeetToMeters, 0)
		tt.anchored = true
	}

	return []position.Traffic{
		{
			Callsign: "TEST1", ICAO: icaoTest1, Lat: lat1, Lon: lon1, AltM: own.AltM,
			TrackTrue: track1, GSKnots: gs1, Airborne: true,
		},
		{
			Callsign: "TEST2", ICAO: icaoTest2, Lat: lat2, Lon: lon2,
			AltM:      own.AltM + passAboveFt*position.FeetToMeters,
			TrackTrue: track2, GSKnots: gs2, Airborne: true,
		},
		{
			Callsign: "TEST3", ICAO: icaoTest3, Lat: tt.anchorLat, Lon: tt.anchorLon,
			AltM: tt.anchorAltM,
		},
	}
}
