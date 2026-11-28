package position

// Airport is an airport of the sim's world, by its code and position.
type Airport struct {
	// Ident is the ICAO code, e.g. EDDP, or the sim's own for one without.
	Ident string
	Lat   float64 // degrees, north positive
	Lon   float64 // degrees, east positive
}
