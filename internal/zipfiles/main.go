// Command zipfiles writes a zip that is the same for the same files: the
// entries get one fixed time and plain permissions, not the build machine's.
//
//	zipfiles <out.zip> <unix time> <file>...
package main

import (
	"archive/zip"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strconv"
	"time"
)

const (
	minArgs   = 4 // the program, the zip, the time and a file
	exitUsage = 2
	fileMode  = 0o644
)

// zipEpochYear is the earliest year a zip entry can hold (MS-DOS time).
const zipEpochYear = 1980

var (
	errTooEarly  = errors.New("time is before 1980, which zip can't hold")
	errDuplicate = errors.New("two files of the same name")
)

func main() {
	if len(os.Args) < minArgs {
		_, _ = fmt.Fprintln(os.Stderr, "usage: zipfiles <out.zip> <unix time> <file>...")

		os.Exit(exitUsage)
	}

	err := run(os.Args[1], os.Args[2], os.Args[3:])
	if err != nil {
		_, _ = fmt.Fprintln(os.Stderr, "zipfiles:", err)

		os.Exit(1)
	}
}

// run writes out from files, with the entries' time given as Unix seconds.
// On failure, out is removed: a broken zip left behind would count as
// built.
func run(out, unix string, files []string) error {
	sec, err := strconv.ParseInt(unix, 10, 64)
	if err != nil {
		return fmt.Errorf("time: %w", err)
	}

	modified := time.Unix(sec, 0).UTC()
	if modified.Year() < zipEpochYear {
		return fmt.Errorf("%w: %s", errTooEarly, modified)
	}

	err = write(out, modified, files)
	if err != nil {
		_ = os.Remove(out)
	}

	return err
}

func write(out string, modified time.Time, files []string) error {
	f, err := os.Create(out)
	if err != nil {
		return fmt.Errorf("create: %w", err)
	}

	archive := zip.NewWriter(f)
	seen := map[string]bool{}

	for _, name := range files {
		base := filepath.Base(name)
		if seen[base] {
			_ = f.Close()

			return fmt.Errorf("%w: %s", errDuplicate, base)
		}

		seen[base] = true

		err = add(archive, name, modified)
		if err != nil {
			_ = f.Close()

			return err
		}
	}

	err = archive.Close()
	if err != nil {
		_ = f.Close()

		return fmt.Errorf("zip: %w", err)
	}

	err = f.Close()
	if err != nil {
		return fmt.Errorf("close: %w", err)
	}

	return nil
}

func add(archive *zip.Writer, name string, modified time.Time) error {
	src, err := os.Open(name)
	if err != nil {
		return err
	}
	defer func() { _ = src.Close() }()

	h := &zip.FileHeader{Name: filepath.Base(name), Method: zip.Deflate, Modified: modified}
	h.SetMode(fileMode)

	w, err := archive.CreateHeader(h)
	if err != nil {
		return fmt.Errorf("%s: %w", name, err)
	}

	_, err = io.Copy(w, src)
	if err != nil {
		return fmt.Errorf("%s: %w", name, err)
	}

	return nil
}
