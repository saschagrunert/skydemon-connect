// Package nmea encodes positions as NMEA 0183 sentences (RMC, GGA, GSA), the
// format serial GPS receivers send.
package nmea

import (
	"fmt"
	"math"
	"strings"
	"time"

	"github.com/saschagrunert/skydemon-connect/internal/position"
)

const (
	minutesPerDegree = 60
	// Minutes are printed with 5 decimals, and counted in those units so
	// that 59.999996' carries into the degrees instead of printing as 60.
	minuteUnits          = 100_000
	unitsPerDegree       = minutesPerDegree * minuteUnits
	latitudeDegreeWidth  = 2
	longitudeDegreeWidth = 3
	trackDecimals        = 1
	speedDecimals        = 1
	altDecimals          = 1
)

// Sentences returns RMC, GGA and GSA sentences for one fix at time t, each
// terminated by CRLF.
//
// A sim has no satellites, but some receivers and EFBs ignore fixes without
// them, so GGA and GSA describe a plausible good 3D fix: GGA fix quality 1
// (GPS), 10 satellites, HDOP 0.8 and a geoid separation of 0 (the altitude
// is MSL already); GSA automatic 3D mode with satellites 01-10 and PDOP 1.2,
// HDOP 0.8, VDOP 0.9.
func Sentences(s position.Sample, t time.Time) string {
	t = t.UTC()
	stamp, date := t.Format("150405.00"), t.Format("020106")
	pos := coordinate(s.Lat, latitudeDegreeWidth, "N", "S") + "," +
		coordinate(s.Lon, longitudeDegreeWidth, "E", "W")

	var b strings.Builder

	b.WriteString(sentence(fmt.Sprintf("GPRMC,%s,A,%s,%.*f,%.*f,%s,,,A",
		stamp, pos, speedDecimals, position.Round(s.GSKnots, speedDecimals),
		trackDecimals, position.RoundAngle(s.TrackTrue, trackDecimals), date)))
	b.WriteString(sentence(fmt.Sprintf("GPGGA,%s,%s,1,10,0.8,%.*f,M,0.0,M,,",
		stamp, pos, altDecimals, position.Round(s.AltM, altDecimals))))
	b.WriteString(sentence("GPGSA,A,3,01,02,03,04,05,06,07,08,09,10,,,1.2,0.8,0.9"))

	return b.String()
}

// checksum returns the two hex digit XOR checksum of a sentence body (the
// text between "$" and "*").
func checksum(body string) string {
	var cs byte
	for i := range len(body) {
		cs ^= body[i]
	}

	return fmt.Sprintf("%02X", cs)
}

func sentence(body string) string {
	return "$" + body + "*" + checksum(body) + "\r\n"
}

// coordinate formats an angle as NMEA (d)ddmm.mmmmm,H with degWidth degree
// digits and the hemisphere letter pos or neg. A value that rounds to zero
// gets pos, so that there is no "0000.00000,S".
func coordinate(v float64, degWidth int, pos, neg string) string {
	units := int64(math.Round(math.Abs(v) * unitsPerDegree))

	hemisphere := pos
	if v < 0 && units != 0 {
		hemisphere = neg
	}

	minutes := units % unitsPerDegree

	return fmt.Sprintf("%0*d%02d.%05d,%s", degWidth, units/unitsPerDegree,
		minutes/minuteUnits, minutes%minuteUnits, hemisphere)
}
