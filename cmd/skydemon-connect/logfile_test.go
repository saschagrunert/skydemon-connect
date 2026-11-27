package main

import (
	"os"
	"path/filepath"
	"runtime"
	"slices"
	"strings"
	"testing"
	"time"
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

// When the previous log can't be moved aside, as when another copy in
// another Windows session has it open, it is left alone: this start logs
// to a file of its own.
func TestOpenLogKeepsBusyLog(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	busy := filepath.Join(dir, logName)

	err := os.WriteFile(busy, []byte("another session\n"), 0o600)
	if err != nil {
		t.Fatal(err)
	}

	// A non-empty directory where the previous log would go makes the
	// move fail, as an open file does on Windows.
	err = os.MkdirAll(filepath.Join(dir, previousLog, "x"), 0o750)
	if err != nil {
		t.Fatal(err)
	}

	_, f, path := openLog(dir)
	if f == nil || path == busy {
		t.Fatalf("log %q", path)
	}

	_ = f.Close()

	got, err := os.ReadFile(busy)
	if err != nil || string(got) != "another session\n" {
		t.Errorf("the other log: %q, %v", got, err)
	}
}

// Per-process logs are cleaned up after a week; the current and previous
// log are not.
func TestRemoveOldLogs(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	week := time.Now().Add(-oldLogAge - time.Hour)

	for _, name := range []string{"SkyDemonConnect.123.log", "SkyDemonConnect.456.log", previousLog, logName} {
		p := filepath.Join(dir, name)

		err := os.WriteFile(p, nil, 0o600)
		if err != nil {
			t.Fatal(err)
		}

		if name != "SkyDemonConnect.456.log" {
			err = os.Chtimes(p, week, week)
			if err != nil {
				t.Fatal(err)
			}
		}
	}

	removeOldLogs(dir, time.Now().Add(-oldLogAge))

	left, _ := filepath.Glob(filepath.Join(dir, "*.log"))
	for i := range left {
		left[i] = filepath.Base(left[i])
	}

	want := []string{"SkyDemonConnect.456.log", logName, previousLog} // sorted
	if !slices.Equal(left, want) {
		t.Errorf("left %q, want %q", left, want)
	}
}
