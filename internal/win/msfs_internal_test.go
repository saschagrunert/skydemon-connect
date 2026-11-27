package win

import "testing"

func TestIsMSFS(t *testing.T) {
	t.Parallel()

	for path, want := range map[string]bool{
		`C:\XboxGames\Microsoft Flight Simulator 2024\Content\FlightSimulator2024.exe`: true,
		`D:\Steam\steamapps\common\MSFS2024\FLIGHTSIMULATOR2024.EXE`:                   true,
		`C:\Program Files\WindowsApps\Microsoft.Limitless\FlightSimulator.exe`:         true,
		`C:\Windows\explorer.exe`:                false,
		`C:\Flight Simulator Tools\launcher.exe`: false,
		"":                                       false,
		`C:\XboxGames\Microsoft Flight Simulator 2024\Co`: false,
		`C:\Tools\FlightSimulatorHelper.exe`:              false,
		`C:\Tools\flightsimulator2024.exe.bak`:            false,
		`FlightSimulator2024.exe`:                         true,
	} {
		if got := isMSFS(path); got != want {
			t.Errorf("isMSFS(%q) = %v, want %v", path, got, want)
		}
	}
}
