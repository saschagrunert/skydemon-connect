//go:build windows

package tray

import (
	"errors"
	"syscall"
	"unsafe"
)

// mutexName is the single-instance mutex, per Windows session.
const mutexName = `Local\SkyDemonConnect`

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
