// Package position holds the aircraft state that flows from the simulator to
// the outputs, in the units and sign conventions the outputs use.
package position

import (
	"math"
	"time"
)

const (
	// KnotsToMetersPerSecond converts knots to m/s: a knot is 1852 m an
	// hour.
	KnotsToMetersPerSecond = 1852.0 / 3600
	// FeetToMeters converts feet to metres.
	FeetToMeters = 0.3048
	// DegPerRad converts radians to degrees.
	DegPerRad = 180 / math.Pi

	fullCircle = 360.0
	halfCircle = 180.0
	maxLat     = 90.0

	// earthRadius is the mean radius of the earth in metres; over the few
	// metres Ahead moves, the earth's flattening doesn't matter.
	earthRadius = 6371008.8
	// minCosLat keeps Ahead away from the poles, where a step east is a
	// division by almost zero.
	minCosLat = 1e-6

	// Positions closer than this to 0°/0° are treated as "no position":
	// SimConnect reports them while the sim loads or sits in its menu.
	nullIslandDegrees = 1e-4
)

// Sample is one snapshot of the user aircraft.
type Sample struct {
	Lat         float64 // degrees, north positive
	Lon         float64 // degrees, east positive
	AltM        float64 // metres MSL
	GSKnots     float64 // ground speed, never negative
	TrackTrue   float64 // ground track, degrees true, [0, 360)
	HeadingTrue float64 // degrees true, [0, 360)
	Pitch       float64 // degrees, nose up positive
	Roll        float64 // degrees, right wing down positive
}

// IsNullIsland reports whether s sits at 0°/0°, which the simulator reports
// when it has no aircraft position.
func (s *Sample) IsNullIsland() bool {
	return math.Abs(s.Lat) < nullIslandDegrees && math.Abs(s.Lon) < nullIslandDegrees
}

// IsFinite reports whether every field of s is a number, neither NaN nor
// infinite, so that the outputs can format it.
func (s *Sample) IsFinite() bool {
	for _, v := range [...]float64{
		s.Lat, s.Lon, s.AltM, s.GSKnots, s.TrackTrue, s.HeadingTrue, s.Pitch, s.Roll,
	} {
		if math.IsNaN(v) || math.IsInf(v, 0) {
			return false
		}
	}

	return true
}

// Ahead returns s moved for d along its track at its ground speed, where a
// GPS fix taken d later would put it. Altitude and attitude stay. A d of
// zero or less, a standing aircraft or one at a pole is not moved.
func (s *Sample) Ahead(d time.Duration) Sample {
	out := *s

	dist := s.GSKnots * KnotsToMetersPerSecond * d.Seconds()
	if dist <= 0 {
		return out
	}

	cosLat := math.Cos(s.Lat / DegPerRad)
	if cosLat < minCosLat {
		return out
	}

	trk := s.TrackTrue / DegPerRad
	lat := s.Lat + dist*math.Cos(trk)/earthRadius*DegPerRad
	lon := s.Lon + dist*math.Sin(trk)/(earthRadius*cosLat)*DegPerRad

	if math.Abs(lat) > maxLat {
		return out
	}

	// Across the antimeridian.
	if lon > halfCircle {
		lon -= fullCircle
	} else if lon < -halfCircle {
		lon += fullCircle
	}

	out.Lat, out.Lon = lat, lon

	return out
}

// Norm360 normalises an angle in degrees to [0, 360), without negative
// zero.
func Norm360(deg float64) float64 {
	deg = math.Mod(deg, fullCircle)
	if deg < 0 {
		deg += fullCircle // a tiny negative angle can round to 360 here
	}

	if deg >= fullCircle || deg == 0 {
		return 0
	}

	return deg
}

// Norm180 normalises an angle in degrees to [-180, 180), e.g. the
// difference of two longitudes, also across the antimeridian.
func Norm180(deg float64) float64 { return Norm360(deg+halfCircle) - halfCircle }

// Round rounds v to decimals places, without negative zero, so that
// printing it with as many decimals shows neither "-0.0" nor a value
// different from the rounded one.
func Round(v float64, decimals int) float64 {
	p := math.Pow10(decimals)

	r := math.Round(v*p) / p
	if r == 0 {
		return 0
	}

	return r
}

// RoundAngle rounds an angle in degrees to decimals places and normalises it
// to [0, 360), so that 359.996 printed with two decimals is "0.00", not
// "360.00".
func RoundAngle(deg float64, decimals int) float64 {
	return Norm360(Round(Norm360(deg), decimals))
}
