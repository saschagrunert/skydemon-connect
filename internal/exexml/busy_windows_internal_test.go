//go:build windows

package exexml

import (
	"os"
	"path/filepath"
	"syscall"
	"testing"
	"time"
)

func TestBusy(t *testing.T) {
	t.Parallel()

	for _, tc := range []struct {
		err  error
		want bool
	}{
		{syscall.Errno(5), true},  // access denied
		{syscall.Errno(32), true}, // sharing violation
		{syscall.Errno(33), true}, // lock violation
		{syscall.Errno(2), false}, // file not found
		{&os.LinkError{Op: "rename", Old: "a", New: "b", Err: syscall.Errno(32)}, true},
	} {
		if got := busy(tc.err); got != tc.want {
			t.Errorf("busy(%v) = %v", tc.err, got)
		}
	}
}

// A file another program holds open without sharing, as a virus scanner
// may for a moment, is replaced once it lets go.
func TestWriteFileWaitsForBusyFile(t *testing.T) {
	t.Parallel()

	path := filepath.Join(t.TempDir(), "exe.xml")

	err := os.WriteFile(path, []byte("old"), 0o600)
	if err != nil {
		t.Fatal(err)
	}

	name, err := syscall.UTF16PtrFromString(path)
	if err != nil {
		t.Fatal(err)
	}

	h, err := syscall.CreateFile(name, syscall.GENERIC_READ, 0, nil, syscall.OPEN_EXISTING, 0, 0)
	if err != nil {
		t.Fatal(err)
	}

	released := make(chan struct{})

	go func() {
		defer close(released)

		time.Sleep(3 * replaceWait)

		_ = syscall.CloseHandle(h)
	}()

	err = writeFile(path, []byte("new"))

	<-released

	if err != nil {
		t.Fatalf("write once the file is free: %v", err)
	}

	if got := readFile(t, path); got != "new" {
		t.Errorf("file holds %q", got)
	}
}
