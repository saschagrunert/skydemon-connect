//go:build windows

package exexml

import (
	"errors"
	"os"
	"syscall"
)

// Windows errors for a file another program has open.
const (
	errAccessDenied     = syscall.Errno(5)
	errSharingViolation = syscall.Errno(32)
	errLockViolation    = syscall.Errno(33)
)

// readOnly reports whether path has the read-only attribute, which Go
// shows as a mode without write permission. Replacing such a file fails
// with access denied, which busy would retry.
func readOnly(path string) bool {
	info, err := os.Stat(path)

	return err == nil && info.Mode().Perm()&0o200 == 0
}

// busy reports whether err is a file another program has open for a
// moment, e.g. a virus scanner, so that trying again is worth it. Access
// denied is among them, as Windows reports it for a file being replaced;
// a real permission problem then shows after the retries, about 1 s later.
func busy(err error) bool {
	return errors.Is(err, errSharingViolation) || errors.Is(err, errLockViolation) ||
		errors.Is(err, errAccessDenied)
}
