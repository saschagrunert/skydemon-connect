package main

import (
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

func TestOpenLog(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()

	l, f, path := openLog(dir)
	if f == nil || path != filepath.Join(dir, logName) {
		t.Fatalf("log %q", path)
	}

	l.Print("first run")

	_ = f.Close()

	// The second start keeps the first one's log.
	l, f, _ = openLog(dir)
	l.Print("second run")

	_ = f.Close()

	prev, err := os.ReadFile(filepath.Join(dir, previousLog))
	if err != nil || !strings.Contains(string(prev), "first run") {
		t.Errorf("previous log %q, %v", prev, err)
	}

	cur, err := os.ReadFile(path)
	if err != nil || strings.Contains(string(cur), "first run") ||
		!strings.Contains(string(cur), "second run") {
		t.Errorf("log %q, %v", cur, err)
	}
}

func TestOpenLogFallback(t *testing.T) {
	t.Parallel()

	if runtime.GOOS == "windows" || os.Getuid() == 0 {
		t.Skip("needs a read-only directory")
	}

	readOnly := filepath.Join(t.TempDir(), "ro")

	err := os.Mkdir(readOnly, 0o500)
	if err != nil {
		t.Fatal(err)
	}

	fallback := t.TempDir()

	_, f, path := openLog(readOnly, fallback)
	if f == nil || path != filepath.Join(fallback, logName) {
		t.Errorf("log %q, want it in the fallback", path)
	}

	if f != nil {
		_ = f.Close()
	}

	l, f, path := openLog(readOnly)
	if f != nil || path != "" {
		t.Errorf("log %q without a writable folder", path)
	}

	l.Print("discarded")
}
