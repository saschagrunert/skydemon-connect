// Package flightplan reads the route of a SkyDemon .flightplan file, the
// format SkyDemon saves routes in.
//
// A saved route holds positions only, no names or airport codes:
//
//	<PrimaryRoute Start="N512526.35 E0121411.00" StartType="Aerodrome" Level="2000" Rules="Vfr">
//	  <RhumbLineRoute To="N513236.00 E0121136.00" ToType="ReportingPoint" Level="MSL" />
//
// so which aerodrome a point is has to be looked up by its position.
package flightplan

import (
	"encoding/xml"
	"errors"
	"fmt"
	"io"
	"strconv"
	"strings"
)

// Point is one point of a route.
type Point struct {
	Lat, Lon float64
	// Aerodrome is whether SkyDemon has the point as an aerodrome.
	Aerodrome bool
}

// Route is the primary route of a flight plan.
type Route struct {
	Points []Point
	// LevelFt is the planned level in feet, 0 when not given. Flight
	// levels are given as feet, e.g. FL55 as 5500.
	LevelFt int
	// VFR is whether the route is planned for VFR.
	VFR bool
}

// minPoints: a start and an end.
const minPoints = 2

var (
	errNoRoute     = errors.New("no PrimaryRoute")
	errTooShort    = errors.New("a route needs at least two points")
	errBadPosition = errors.New("bad position")
)

// doc is the part of a .flightplan that matters here.
type doc struct {
	Route *struct {
		Start     string `xml:"Start,attr"`
		StartType string `xml:"StartType,attr"`
		Level     string `xml:"Level,attr"`
		Rules     string `xml:"Rules,attr"`
		// Legs are every element with a To, whatever the course type:
		// RhumbLineRoute and its siblings.
		Legs []struct {
			To     string `xml:"To,attr"`
			ToType string `xml:"ToType,attr"`
		} `xml:",any"`
	} `xml:"PrimaryRoute"`
}

// Parse reads the primary route of a .flightplan.
func Parse(r io.Reader) (Route, error) {
	var d doc

	err := xml.NewDecoder(r).Decode(&d)
	if err != nil {
		return Route{}, fmt.Errorf("read flight plan: %w", err)
	}

	if d.Route == nil {
		return Route{}, errNoRoute
	}

	out := Route{
		Points:  nil,
		LevelFt: level(d.Route.Level),
		VFR:     strings.EqualFold(d.Route.Rules, "Vfr"),
	}

	start, err := point(d.Route.Start, d.Route.StartType)
	if err != nil {
		return Route{}, fmt.Errorf("start: %w", err)
	}

	out.Points = append(out.Points, start)

	for _, leg := range d.Route.Legs {
		if leg.To == "" {
			continue // the weight and balance, airfields and such
		}

		p, err := point(leg.To, leg.ToType)
		if err != nil {
			// The start is point 0.
			return Route{}, fmt.Errorf("point %d: %w", len(out.Points), err)
		}

		out.Points = append(out.Points, p)
	}

	if len(out.Points) < minPoints {
		return Route{}, errTooShort
	}

	return out, nil
}

func point(pos, typ string) (Point, error) {
	lat, lon, err := position(pos)
	if err != nil {
		return Point{}, err
	}

	return Point{Lat: lat, Lon: lon, Aerodrome: strings.EqualFold(typ, "Aerodrome")}, nil
}

const feetPerFlightLevel = 100

// level is a route's level in feet: 2000, or FL55 as 5500. Anything else,
// e.g. "MSL", is not given.
func level(s string) int {
	s = strings.TrimSpace(s)
	scale := 1

	if rest, ok := strings.CutPrefix(strings.ToUpper(s), "FL"); ok {
		s, scale = rest, feetPerFlightLevel
	}

	v, err := strconv.Atoi(s)
	if err != nil || v <= 0 {
		return 0
	}

	return v * scale
}

const (
	// positionParts: latitude and longitude.
	positionParts = 2
	latDegDigits  = 2
	lonDegDigits  = 3
	maxLat        = 90
	maxLon        = 180
)

// position parses "N452346.00 E0115053.00": degrees, minutes and seconds.
func position(s string) (float64, float64, error) {
	parts := strings.Fields(s)
	if len(parts) != positionParts {
		return 0, 0, fmt.Errorf("%w %q", errBadPosition, s)
	}

	lat, latErr := angle(parts[0], 'N', 'S', latDegDigits, maxLat)
	lon, lonErr := angle(parts[1], 'E', 'W', lonDegDigits, maxLon)

	if latErr != nil || lonErr != nil {
		return 0, 0, fmt.Errorf("%w %q", errBadPosition, s)
	}

	return lat, lon, nil
}

const (
	minuteDigits     = 2
	minutesPerDegree = 60
	secondsPerDegree = 3600
)

// angle parses one hemisphere letter, then degDigits digits of degrees, two
// of minutes and the seconds, with decimals.
func angle(s string, pos, neg byte, degDigits int, limit float64) (float64, error) {
	if len(s) < 1+degDigits+minuteDigits+minuteDigits {
		return 0, errBadPosition
	}

	// Atoi would take a sign: "N-10000.00" is no position.
	if !digits(s[1 : 1+degDigits+minuteDigits]) {
		return 0, errBadPosition
	}

	sign, err := hemisphere(s[0], pos, neg)
	if err != nil {
		return 0, err
	}

	digits := s[1:]

	deg, degErr := strconv.Atoi(digits[:degDigits])
	minutes, minErr := strconv.Atoi(digits[degDigits : degDigits+minuteDigits])
	sec, secErr := strconv.ParseFloat(digits[degDigits+minuteDigits:], 64)

	if errors.Join(degErr, minErr, secErr) != nil {
		return 0, errBadPosition
	}

	// NaN fails every comparison, so it is caught by asking for what is valid.
	if minutes < 0 || minutes >= minutesPerDegree || !(sec >= 0 && sec < minutesPerDegree) {
		return 0, errBadPosition
	}

	v := float64(deg) + float64(minutes)/minutesPerDegree + sec/secondsPerDegree
	if v > limit {
		return 0, errBadPosition
	}

	return sign * v, nil
}

// digits reports whether s is decimal digits only.
func digits(s string) bool {
	for _, c := range []byte(s) {
		if c < '0' || c > '9' {
			return false
		}
	}

	return true
}

// hemisphere is 1 for pos, -1 for neg.
func hemisphere(c, pos, neg byte) (float64, error) {
	switch c {
	case pos:
		return 1, nil
	case neg:
		return -1, nil
	default:
		return 0, errBadPosition
	}
}
