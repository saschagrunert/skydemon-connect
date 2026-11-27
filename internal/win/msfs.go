package win

import "strings"

// isMSFS reports whether path is an MSFS executable: FlightSimulator2024.exe,
// or FlightSimulator.exe of MSFS 2020, in any case and folder.
func isMSFS(path string) bool {
	// Split by hand: filepath only knows backslashes on Windows, and this
	// is tested everywhere.
	name := path[strings.LastIndexAny(path, `\/`)+1:]

	return strings.HasPrefix(strings.ToLower(name), "flightsimulator")
}
