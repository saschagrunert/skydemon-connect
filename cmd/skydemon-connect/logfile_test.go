package main

import (
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"
)

// writeLog writes text to the log openLog opens in dir and returns its
// path.
func writeLog(t *testing.T, dir, text string) string {
	t.Helper()

	f := openLog(dir)
	if f == nil {
		t.Fatal("no log")
	}

	newLogger(f).Print(text)

	_ = f.Close()

	return f.Name()
}

func TestOpenLog(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()

	if path := writeLog(t, dir, "first run"); path != filepath.Join(dir, logName) {
		t.Fatalf("log %q", path)
	}

	// The second start keeps the first one's log.
	path := writeLog(t, dir, "second run")

	prev, err := os.ReadFile(filepath.Join(dir, previousLog))
	if err != nil || !strings.HasSuffix(string(prev), " first run\n") {
		t.Errorf("previous log %q, %v", prev, err)
	}

	cur, err := os.ReadFile(path)
	if err != nil || !strings.HasSuffix(string(cur), " second run\n") {
		t.Errorf("log %q, %v", cur, err)
	}
}

func TestOpenLogFallback(t *testing.T) {
	t.Parallel()

	// A file where the folder should be: no log can go there, on any OS
	// and as any user, which a read-only folder doesn't give.
	readOnly := filepath.Join(t.TempDir(), "not-a-folder")

	err := os.WriteFile(readOnly, nil, 0o600)
	if err != nil {
		t.Fatal(err)
	}

	fallback := t.TempDir()

	f := openLog(readOnly, fallback)
	if f == nil || f.Name() != filepath.Join(fallback, logName) {
		t.Errorf("log %v, want it in the fallback", f)
	}

	if f != nil {
		_ = f.Close()
	}

	f = openLog(readOnly)
	if f != nil {
		t.Errorf("log %q without a writable folder", f.Name())
	}

	newLogger(f).Print("discarded")
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

	if path := writeLog(t, dir, "this one"); path == busy || !isPerProcessLog(path) {
		t.Fatalf("log %q", path)
	}

	got, err := os.ReadFile(busy)
	if err != nil || string(got) != "another session\n" {
		t.Errorf("the other log: %q, %v", got, err)
	}
}

// A log another copy created between our move and our create is not
// truncated: the exclusive create fails, and this start gets its own.
func TestCreateLogExclusive(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	theirs := filepath.Join(dir, logName)

	// The move finds nothing, and the other copy creates its log right
	// after: it is there when this one creates its own.
	rename := func(string, string) error {
		err := os.WriteFile(theirs, []byte("other copy\n"), 0o600)
		if err != nil {
			t.Fatal(err)
		}

		return os.ErrNotExist
	}

	f, err := createLogWith(dir, rename)
	if err != nil {
		t.Fatal(err)
	}

	_ = f.Close()

	if !isPerProcessLog(f.Name()) {
		t.Errorf("log %q, want a per-process one", f.Name())
	}

	got, err := os.ReadFile(theirs)
	if err != nil || string(got) != "other copy\n" {
		t.Errorf("the other copy's log: %q, %v", got, err)
	}
}

func isPerProcessLog(path string) bool {
	return strings.HasPrefix(filepath.Base(path), logStem+".") &&
		filepath.Base(path) != logName && filepath.Base(path) != previousLog
}

// Per-process logs are cleaned up after a week; the current and previous
// log are not, nor a log kept under another name.
func TestRemoveOldLogs(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	week := time.Now().Add(-oldLogAge - time.Hour)
	kept := "SkyDemonConnect.crash-2026-05.log"

	for _, name := range []string{
		"SkyDemonConnect.123.log", "SkyDemonConnect.456.log", previousLog, logName, kept,
	} {
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

	want := []string{"SkyDemonConnect.456.log", kept, logName, previousLog} // sorted
	if !slices.Equal(left, want) {
		t.Errorf("left %q, want %q", left, want)
	}
}
