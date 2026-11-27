//go:build !windows

package exexml

// busy reports whether err is a file another program has open for a
// moment. Elsewhere than on Windows, renaming over an open file works.
func busy(error) bool { return false }
