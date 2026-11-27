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

// A file goes in under its base name unless the argument names it.
func TestNames(t *testing.T) {
	t.Parallel()

	inputs := files(t, "a.exe", "reference.md")
	out := filepath.Join(t.TempDir(), "out.zip")

	err := run(out, "1764230400", []string{inputs[0], inputs[1] + "=docs/reference.md"})
	if err != nil {
		t.Fatal(err)
	}

	r, err := zip.OpenReader(out)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = r.Close() }()

	names := make([]string, 0, len(r.File))
	for _, f := range r.File {
		names = append(names, f.Name)
	}

	if len(names) != 2 || names[0] != "a.exe" || names[1] != "docs/reference.md" {
		t.Errorf("names %q, want [a.exe docs/reference.md]", names)
	}
}

// A file whose path has = is that file, while a name in the zip must be
// relative and free of . and .., with \ taken as /.
func TestEntry(t *testing.T) {
	t.Parallel()

	withEq := files(t, "a=b")[0]
	missing := filepath.Join(t.TempDir(), "x")

	for arg, want := range map[string][2]string{
		withEq:                {withEq, "a=b"},
		withEq + "=docs/a.md": {withEq, "docs/a.md"},
		"f=docs\\a.md":        {"f", "docs/a.md"},
		missing:               {missing, "x"},
	} {
		src, name, err := entry(arg)
		if err != nil || src != want[0] || name != want[1] {
			t.Errorf("%s: %q %q %v, want %q %q", arg, src, name, err, want[0], want[1])
		}
	}

	for _, arg := range []string{"f=", "f=/etc/x", "f=../x", "f=a/../x", "f=.", "f=a//b", "f=a\\..\\..\\x"} {
		_, _, err := entry(arg)
		if !errors.Is(err, errBadName) {
			t.Errorf("%s: %v, want %v", arg, err, errBadName)
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
