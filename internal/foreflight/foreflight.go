// Package foreflight encodes the ForeFlight simulator UDP protocol (XGPS and
// XATT messages on port 49002), which SkyDemon reads as "X-Plane".
//
// See https://foreflight.com/support/network-gps for the specification.
package foreflight

import (
	"fmt"

	"github.com/saschagrunert/skydemon-connect/internal/position"
)

// Port is the UDP port EFB apps listen on for simulator data.
const Port = 49002

// Decimals the angles are rounded and printed to. Rounding first wraps
// 359.996° to 0 rather than printing 360.00.
const (
	trackDecimals    = 2
	attitudeDecimals = 1
)

// XGPS returns a position message:
// XGPS<name>,lon,lat,alt_m,track_true,groundspeed_mps.
//
// The specification suggests 2-3 decimals for 1 Hz updates; at 30 Hz the
// aircraft moves only a few metres between messages, so more are sent.
func XGPS(name string, s position.Sample) string {
	return fmt.Sprintf("XGPS%s,%.6f,%.6f,%.1f,%.*f,%.2f",
		name, s.Lon, s.Lat, s.AltM,
		trackDecimals, position.RoundAngle(s.TrackTrue, trackDecimals),
		s.GSKnots*position.KnotsToMetersPerSecond)
}

// XATT returns an attitude message: XATT<name>,heading_true,pitch,roll with
// pitch up and roll right positive.
func XATT(name string, s position.Sample) string {
	const d = attitudeDecimals

	return fmt.Sprintf("XATT%s,%.*f,%.*f,%.*f", name,
		d, position.RoundAngle(s.HeadingTrue, d),
		d, position.Round(s.Pitch, d),
		d, position.Round(s.Roll, d))
}
