// Command zipfiles writes a zip that is the same for the same files: the
// entries get one fixed time and plain permissions, not the build machine's.
//
//	zipfiles <out.zip> <unix time> <file>...
package main

import (
	"archive/zip"
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

func main() {
	if len(os.Args) < minArgs {
		_, _ = fmt.Fprintln(os.Stderr, "usage: zipfiles <out.zip> <unix time> <file>...")

		os.Exit(exitUsage)
	}

	sec, err := strconv.ParseInt(os.Args[2], 10, 64)
	if err == nil {
		err = write(os.Args[1], time.Unix(sec, 0).UTC(), os.Args[3:])
		if err != nil {
			// A broken zip left behind would count as built.
			_ = os.Remove(os.Args[1])
		}
	}

	if err != nil {
		_, _ = fmt.Fprintln(os.Stderr, "zipfiles:", err)

		os.Exit(1)
	}
}

func write(out string, modified time.Time, files []string) error {
	f, err := os.Create(out)
	if err != nil {
		return fmt.Errorf("create: %w", err)
	}

	archive := zip.NewWriter(f)

	for _, name := range files {
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
