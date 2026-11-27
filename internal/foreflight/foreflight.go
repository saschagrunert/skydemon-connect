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

// Decimals the values are rounded and printed to. Rounding first wraps
// 359.996° to 0 rather than printing 360.00, and avoids "-0.0".
const (
	coordDecimals    = 6
	altDecimals      = 1
	trackDecimals    = 2
	speedDecimals    = 2
	attitudeDecimals = 1
)

// XGPS returns a position message:
// XGPS<name>,lon,lat,alt_m,track_true,groundspeed_mps.
//
// The specification suggests 2-3 decimals for 1 Hz updates; at 30 Hz the
// aircraft moves only a few metres between messages, so more are sent.
func XGPS(name string, s position.Sample) string {
	return fmt.Sprintf(
		"XGPS%s,%.*f,%.*f,%.*f,%.*f,%.*f",
		name,
		coordDecimals,
		position.Round(s.Lon, coordDecimals),
		coordDecimals,
		position.Round(s.Lat, coordDecimals),
		altDecimals,
		position.Round(s.AltM, altDecimals),
		trackDecimals,
		position.RoundAngle(s.TrackTrue, trackDecimals),
		speedDecimals,
		position.Round(s.GSKnots*position.KnotsToMetersPerSecond, speedDecimals),
	)
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

// XTRAFFIC returns a traffic message:
// XTRAFFIC<name>,icao,lat,lon,alt_ft,vs_fpm,airborne,track_true,speed_kt,callsign.
func XTRAFFIC(name string, t position.Traffic) string {
	airborne := 0
	if t.Airborne {
		airborne = 1
	}

	return fmt.Sprintf(
		"XTRAFFIC%s,%d,%.*f,%.*f,%.*f,%.*f,%d,%.*f,%.*f,%s",
		name,
		t.ICAO,
		coordDecimals,
		position.Round(t.Lat, coordDecimals),
		coordDecimals,
		position.Round(t.Lon, coordDecimals),
		altDecimals,
		position.Round(t.AltM/position.FeetToMeters, altDecimals),
		altDecimals,
		position.Round(t.VSFpm, altDecimals),
		airborne,
		trackDecimals,
		position.RoundAngle(t.TrackTrue, trackDecimals),
		speedDecimals,
		position.Round(t.GSKnots, speedDecimals),
		t.Callsign,
	)
}
