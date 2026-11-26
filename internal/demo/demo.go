// Package demo produces a fake flight, a constant-rate circle, for testing
// an EFB without starting the simulator.
package demo

import (
	"context"
	"math"
	"time"

	"github.com/saschagrunert/skydemon-connect/internal/position"
)

const (
	radiusNM      = 3.0
	groundSpeedKt = 100.0
	altitudeFt    = 2500.0
	framesPerSec  = 60
	gravity       = 9.81
	// A nautical mile is one minute of latitude.
	nmPerDegree   = 60.0
	secondsPerH   = 3600.0
	quarterCircle = 90.0
)

// wrapLon maps a longitude into [-180, 180), for circles across the
// antimeridian.
func wrapLon(lon float64) float64 {
	const halfCircle = 180.0

	return position.Norm360(lon+halfCircle) - halfCircle
}

// at returns the sample t into a clockwise circle of 3 NM radius at 100 kt
// and 2500 ft around lat/lon, starting due north of the centre.
func at(lat, lon float64, t time.Duration) position.Sample {
	omega := groundSpeedKt / radiusNM / secondsPerH // rad/s
	bank := math.Atan(
		groundSpeedKt*position.KnotsToMetersPerSecond*omega/gravity,
	) * position.DegPerRad
	a := omega * t.Seconds() // bearing from the centre, clockwise from north
	trk := position.Norm360(a*position.DegPerRad + quarterCircle)

	return position.Sample{
		Lat: lat + radiusNM/nmPerDegree*math.Cos(a),
		Lon: wrapLon(
			lon + radiusNM/nmPerDegree*math.Sin(a)/math.Cos(lat/position.DegPerRad),
		),
		AltM:        altitudeFt * position.FeetToMeters,
		GSKnots:     groundSpeedKt,
		TrackTrue:   trk,
		HeadingTrue: trk,
		Pitch:       0,
		Roll:        bank,
	}
}

// Run calls update at 60 Hz, like a sim would, until ctx is done.
func Run(ctx context.Context, lat, lon float64, update func(position.Sample)) {
	start := time.Now()

	tick := time.NewTicker(time.Second / framesPerSec)
	defer tick.Stop()

	for {
		select {
		case <-ctx.Done():
			return
		case now := <-tick.C:
			update(at(lat, lon, now.Sub(start)))
		}
	}
}
