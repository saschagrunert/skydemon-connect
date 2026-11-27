package position

// Traffic is another aircraft, as an EFB's traffic display needs it.
type Traffic struct {
	// Callsign is shown next to the target; no commas.
	Callsign  string
	Lat       float64 // degrees, north positive
	Lon       float64 // degrees, east positive
	AltM      float64 // metres MSL
	VSFpm     float64 // vertical speed, feet per minute, climb positive
	TrackTrue float64 // degrees true, [0, 360)
	GSKnots   float64 // ground speed
	// ICAO is the 24-bit transponder address, unique per target.
	ICAO     uint32
	Airborne bool
}
