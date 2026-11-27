// Command zipfiles writes a zip that is the same for the same files: the
// entries get one fixed time and plain permissions, not the build machine's.
//
//	zipfiles <out.zip> <unix time> <file>[=<name in zip>]...
//
// A file goes into the zip under its base name, or under the given name,
// e.g. docs/reference.md=docs/reference.md to keep its folder. An argument
// that names an existing file is that file, = and all; otherwise the last =
// splits it, so a name in the zip can't contain =.
package main

import (
	"archive/zip"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"
)

const (
	minArgs   = 4 // the program, the zip, the time and a file
	exitUsage = 2
	fileMode  = 0o644
)

const usage = "usage: zipfiles <out.zip> <unix time> <file>[=<name in zip>]..."

// zipEpochYear is the earliest year a zip entry can hold (MS-DOS time).
const zipEpochYear = 1980

var (
	errTooEarly  = errors.New("time is before 1980, which zip can't hold")
	errDuplicate = errors.New("two files of the same name")
	errBadName   = errors.New("zip name must be relative, without . or .. elements")
)

func main() {
	if len(os.Args) < minArgs {
		_, _ = fmt.Fprintln(os.Stderr, usage)

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

	for _, arg := range files {
		var src, name string

		src, name, err = entry(arg)
		if err != nil {
			_ = f.Close()

			return err
		}

		if seen[name] {
			_ = f.Close()

			return fmt.Errorf("%w: %s", errDuplicate, name)
		}

		seen[name] = true

		err = add(archive, src, name, modified)
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

// entry splits a <file>[=<name in zip>] argument into the file and its
// name in the zip, which uses forward slashes, also for a \ on Linux.
func entry(arg string) (string, string, error) {
	src, name := arg, filepath.Base(arg)

	i := strings.LastIndex(arg, "=")

	_, err := os.Stat(arg)
	if i >= 0 && errors.Is(err, fs.ErrNotExist) {
		src, name = arg[:i], arg[i+1:]
	}

	name = strings.ReplaceAll(name, `\`, "/")
	if !fs.ValidPath(name) || name == "." || !filepath.IsLocal(filepath.FromSlash(name)) {
		return "", "", fmt.Errorf("%w: %q", errBadName, name)
	}

	return src, name, nil
}

func add(archive *zip.Writer, src, name string, modified time.Time) error {
	file, err := os.Open(src)
	if err != nil {
		return err
	}
	defer func() { _ = file.Close() }()

	h := &zip.FileHeader{Name: name, Method: zip.Deflate, Modified: modified}
	h.SetMode(fileMode)

	w, err := archive.CreateHeader(h)
	if err != nil {
		return fmt.Errorf("%s: %w", src, err)
	}

	_, err = io.Copy(w, file)
	if err != nil {
		return fmt.Errorf("%s: %w", src, err)
	}

	return nil
}
