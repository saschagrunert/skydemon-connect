//go:build windows

package win

import "syscall"

// Minimal Win32 bindings for the helpers.

var (
	user32   = syscall.NewLazyDLL("user32.dll")
	kernel32 = syscall.NewLazyDLL("kernel32.dll")

	procMessageBoxW  = user32.NewProc("MessageBoxW")
	procCreateMutexW = kernel32.NewProc("CreateMutexW")
	procAllocConsole = kernel32.NewProc("AllocConsole")
)

// UTF16Ptr converts s for a Win32 call. Our strings never contain NUL;
// if one did, the call gets an empty string.
func UTF16Ptr(s string) *uint16 {
	p, err := syscall.UTF16PtrFromString(s)
	if err != nil {
		return &[]uint16{0}[0]
	}

	return p
}
