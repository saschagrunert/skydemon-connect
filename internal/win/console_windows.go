//go:build windows

package win

import (
	"fmt"
	"os"
)

// Console opens a console window for the GUI-subsystem process and
// returns it. Not the console it was started from: PowerShell does not
// wait for a GUI program, so its prompt would mix with the output.
func Console() (*os.File, error) {
	r, _, err := procAllocConsole.Call()
	if r == 0 {
		return nil, fmt.Errorf("AllocConsole: %w", err)
	}

	f, err := os.OpenFile("CONOUT$", os.O_RDWR, 0)
	if err != nil {
		return nil, fmt.Errorf("console: %w", err)
	}

	return f, nil
}
