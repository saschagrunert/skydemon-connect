//go:build !windows

package exexml

// readOnly reports whether path is read-only in a way that stops it from
// being replaced: elsewhere than on Windows, renaming over it works.
func readOnly(string) bool { return false }

// busy reports whether err is a file another program has open for a
// moment. Elsewhere than on Windows, renaming over an open file works.
func busy(error) bool { return false }
