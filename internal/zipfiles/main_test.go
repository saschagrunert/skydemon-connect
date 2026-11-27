package main

import (
	"archive/zip"
	"bytes"
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"
)

// files writes the named files with some content and returns their paths.
func files(t *testing.T, names ...string) []string {
	t.Helper()

	dir := t.TempDir()
	paths := make([]string, len(names))

	for i, name := range names {
		paths[i] = filepath.Join(dir, name)

		err := os.WriteFile(paths[i], []byte("content of "+name), 0o600)
		if err != nil {
			t.Fatal(err)
		}
	}

	return paths
}

// The same files give the same bytes, whatever their times on disk.
func TestReproducible(t *testing.T) {
	t.Parallel()

	inputs := files(t, "a.exe", "LICENSE")
	out := t.TempDir()
	first, second := filepath.Join(out, "1.zip"), filepath.Join(out, "2.zip")

	err := run(first, "1764230400", inputs)
	if err != nil {
		t.Fatal(err)
	}

	later := time.Now().Add(time.Hour)

	err = os.Chtimes(inputs[0], later, later)
	if err != nil {
		t.Fatal(err)
	}

	err = run(second, "1764230400", inputs)
	if err != nil {
		t.Fatal(err)
	}

	a, _ := os.ReadFile(first)
	b, _ := os.ReadFile(second)

	if len(a) == 0 || !bytes.Equal(a, b) {
		t.Error("two runs gave different zips")
	}

	r, err := zip.OpenReader(first)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = r.Close() }()

	for _, f := range r.File {
		if f.Mode().Perm() != fileMode || !f.Modified.Equal(time.Unix(1764230400, 0)) {
			t.Errorf("%s: mode %v, time %v", f.Name, f.Mode(), f.Modified)
		}
	}
}

// A failed run leaves no zip behind, which make would take as built.
func TestFailureRemovesZip(t *testing.T) {
	t.Parallel()

	inputs := files(t, "LICENSE")
	other := files(t, "LICENSE")

	for name, tc := range map[string]struct {
		unix  string
		files []string
		want  error
	}{
		"missing file":    {"1764230400", []string{filepath.Join(t.TempDir(), "gone")}, os.ErrNotExist},
		"duplicate name":  {"1764230400", append(inputs, other...), errDuplicate},
		"time 0":          {"0", inputs, errTooEarly},
		"time not a time": {"yesterday", inputs, nil},
	} {
		out := filepath.Join(t.TempDir(), "out.zip")

		err := run(out, tc.unix, tc.files)
		if err == nil || (tc.want != nil && !errors.Is(err, tc.want)) {
			t.Errorf("%s: %v, want %v", name, err, tc.want)
		}

		_, err = os.Stat(out)
		if !errors.Is(err, os.ErrNotExist) {
			t.Errorf("%s: the zip was left behind", name)
		}
	}
}
