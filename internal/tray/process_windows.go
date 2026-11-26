//go:build windows

package tray

import (
	"errors"
	"fmt"
	"os"
	"syscall"
	"unsafe"
)

// mutexName is the single-instance mutex, per Windows session.
const mutexName = `Local\SkyDemonConnect`

func messageBox(text string, flags uintptr) {
	t, c := utf16Ptr(text), utf16Ptr("SkyDemonConnect")
	_, _, _ = procMessageBoxW.Call(0, uintptr(unsafe.Pointer(t)), uintptr(unsafe.Pointer(c)), flags)
}

// ShowError shows a modal error dialog.
func ShowError(text string) { messageBox(text, mbIconError) }

// ShowInfo shows a modal information dialog.
func ShowInfo(text string) { messageBox(text, mbIconInfo) }

// AlreadyRunning creates the single-instance mutex and reports whether
// another instance holds it. The mutex lives as long as the process.
func AlreadyRunning() bool { return alreadyRunning(mutexName) }

func alreadyRunning(mutex string) bool {
	name := utf16Ptr(mutex)
	_, _, err := procCreateMutexW.Call(0, 0, uintptr(unsafe.Pointer(name)))

	// Access is denied when an instance with a higher integrity level, e.g.
	// one MSFS started as administrator, holds the mutex.
	return errors.Is(err, syscall.Errno(errAlreadyExists)) ||
		errors.Is(err, syscall.Errno(errAccessDenied))
}

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
