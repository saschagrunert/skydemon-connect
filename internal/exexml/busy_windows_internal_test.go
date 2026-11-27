//go:build windows

package exexml

import (
	"errors"
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
// may for a moment, is replaced once it lets go. The other program lets go
// during the first wait, so the test doesn't depend on how long the
// retries take on a slow machine.
func TestReplaceWaitsForBusyFile(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	path, tmp := filepath.Join(dir, "exe.xml"), filepath.Join(dir, "exe.xml.new")

	for p, text := range map[string]string{path: "old", tmp: "new"} {
		err := os.WriteFile(p, []byte(text), 0o600)
		if err != nil {
			t.Fatal(err)
		}
	}

	name, err := syscall.UTF16PtrFromString(path)
	if err != nil {
		t.Fatal(err)
	}

	h, err := syscall.CreateFile(name, syscall.GENERIC_READ, 0, nil, syscall.OPEN_EXISTING, 0, 0)
	if err != nil {
		t.Fatal(err)
	}

	waits := 0
	letGo := func(time.Duration) {
		if waits == 0 {
			_ = syscall.CloseHandle(h)
		}

		waits++
	}

	err = replace(tmp, path, os.Rename, busy, letGo)
	if err != nil || waits == 0 {
		t.Fatalf("replace once the file is free: %v after %d waits", err, waits)
	}

	if got := readFile(t, path); got != "new" {
		t.Errorf("file holds %q", got)
	}
}

// A read-only exe.xml is reported as such at once, not retried as busy.
func TestSetReadOnly(t *testing.T) {
	t.Parallel()

	_, store, _ := setup(t)

	err := os.WriteFile(store, []byte(sample), 0o600)
	if err != nil {
		t.Fatal(err)
	}

	err = os.Chmod(store, 0o400) // sets FILE_ATTRIBUTE_READONLY
	if err != nil {
		t.Fatal(err)
	}

	t.Cleanup(func() { _ = os.Chmod(store, 0o600) })

	start := time.Now()

	err = (Installs{store}).Set(true, `C:\x.exe`, nil)
	if !errors.Is(err, errReadOnly) || time.Since(start) >= replaceWait {
		t.Errorf("after %v: %v", time.Since(start), err)
	}

	if got := readFile(t, store); got != sample {
		t.Error("the read-only file was changed")
	}
}

// A read-only exe.xml without our entry is left out of State, as Set
// can't add to it: the other file's entry reads as Ours, and can be
// removed.
func TestStateLeavesOutReadOnly(t *testing.T) {
	t.Parallel()

	installs, store, steam := setup(t)
	exe := `C:\x.exe`

	err := os.WriteFile(steam, []byte(sample), 0o400) // FILE_ATTRIBUTE_READONLY
	if err != nil {
		t.Fatal(err)
	}

	t.Cleanup(func() { _ = os.Chmod(steam, 0o600) })

	err = (Installs{store}).Set(true, exe, nil)
	if err != nil {
		t.Fatal(err)
	}

	if got := installs.State(exe, nil); got != Ours {
		t.Errorf("with a read-only file: %v, want Ours", got)
	}
}
