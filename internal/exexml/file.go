package exexml

import (
	"bytes"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"time"
)

// inFile names path in err, unless an error from the os package inside it
// already does.
func inFile(path string, err error) error {
	_, isPath := errors.AsType[*fs.PathError](err)
	_, isLink := errors.AsType[*os.LinkError](err)

	if isPath || isLink {
		return err
	}

	return fmt.Errorf("%s: %w", path, err)
}

func update(path string, enable bool, exePath, commandLine string) error {
	// A symlinked exe.xml stays a symlink: the file it points to is
	// edited.
	path, err := resolve(path)
	if err != nil {
		return err
	}

	orig, err := readRaw(path)
	if err != nil {
		return err
	}

	doc, enc, err := decodeFile(orig)
	if err != nil {
		return err
	}

	existed := doc != ""
	if !existed && !enable {
		return nil // nothing to remove, and no reason to create the file
	}

	out, err := edited(doc, enable, exePath, commandLine, enc)
	if err != nil {
		return err
	}

	if out == doc {
		return nil
	}

	b, err := enc.encode(out)
	if err != nil {
		return err
	}

	return save(path, orig, b, existed)
}

// save writes b over the file at path that held orig, after backing up an
// existing one. It is checked for another installer's change before the
// backup, so that the backup is of what this change starts from, and again
// before the write, so as not to undo one made during the backup.
func save(path string, orig, b []byte, existed bool) error {
	err := unchanged(path, orig)
	if err != nil {
		return err
	}

	if existed {
		err = backup(path, orig)
		if err != nil {
			return fmt.Errorf("backup: %w", err)
		}
	}

	err = unchanged(path, orig)
	if err != nil {
		return err
	}

	return writeFile(path, b)
}

// unchanged returns errChanged unless path still holds orig: another
// installer changed it meanwhile, whose change mustn't be undone.
func unchanged(path string, orig []byte) error {
	now, err := readRaw(path)
	if err != nil {
		return err
	}

	if !bytes.Equal(now, orig) {
		return errChanged
	}

	return nil
}

// resolve follows symlinks to the file path names. A missing file stays as
// it is, but a symlink that doesn't resolve is refused: replacing it with
// a new file would lose the link, e.g. to a synced drive not mounted now.
func resolve(path string) (string, error) {
	info, err := os.Lstat(path)
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return path, nil
		}

		return "", err
	}

	if info.Mode()&fs.ModeSymlink == 0 {
		// Not a link itself; a linked folder above it is followed too.
		target, err := filepath.EvalSymlinks(path)
		if err != nil {
			return path, nil //nolint:nilerr // the file is there, as it is
		}

		return target, nil
	}

	target, err := filepath.EvalSymlinks(path)
	if err != nil {
		return "", errBrokenLink
	}

	return target, nil
}

// edited returns doc with our entry added or removed; an empty doc starts
// as the skeleton.
func edited(doc string, enable bool, exePath, commandLine string, enc encoding) (string, error) {
	if doc == "" {
		doc = skeleton
	}

	if !enable {
		return remove(doc), nil
	}

	return add(doc, exePath, commandLine, enc)
}

// backup keeps orig, the file at path as read before a change, twice: as
// prevExt, replaced before every change, so that the latest one can be
// undone, and as backupExt, written once before the first change ever and
// kept from then on.
func backup(path string, orig []byte) error {
	bak := path + backupExt

	info, err := os.Stat(bak)

	switch {
	case err == nil && !info.Mode().IsRegular():
		return fmt.Errorf("%s %w", filepath.Base(bak), errNotFile)
	case errors.Is(err, fs.ErrNotExist):
		err = writeFile(bak, orig)
		if err != nil {
			return err
		}
	case err != nil:
		return err
	}

	prev := path + prevExt

	info, err = os.Stat(prev)
	if err == nil && !info.Mode().IsRegular() {
		return fmt.Errorf("%s %w", filepath.Base(prev), errNotFile)
	}

	return writeFile(prev, orig)
}

// writeFile replaces path with b atomically: a crash or a full disk leaves
// either the old file or the new one, never a truncated one.
func writeFile(path string, b []byte) error {
	f, err := os.CreateTemp(filepath.Dir(path), filepath.Base(path)+".*.tmp")
	if err != nil {
		return fmt.Errorf("write: %w", err)
	}

	tmp := f.Name()

	_, err = f.Write(b)
	if err == nil {
		err = f.Sync()
	}

	err = errors.Join(err, f.Close())
	if err == nil {
		err = os.Chmod(tmp, fileMode)
	}

	if err == nil {
		err = replace(tmp, path, os.Rename, busy, time.Sleep)
	}

	if err != nil {
		_ = os.Remove(tmp)

		return fmt.Errorf("write: %w", err)
	}

	return nil
}

// replace renames tmp to path, which replaces path, also on Windows. A
// file another program has open, e.g. a virus scanner just checking the
// new one, can't be replaced for a moment: busy errors are retried for
// about a second.
func replace(
	tmp, path string,
	rename func(string, string) error,
	busy func(error) bool,
	sleep func(time.Duration),
) error {
	var err error

	for try := range replaceTries {
		if try > 0 {
			sleep(replaceWait)
		}

		err = rename(tmp, path)
		if err == nil || !busy(err) {
			return err
		}
	}

	return err
}

// read returns the file's text and encoding. A missing or blank file reads
// as empty, in the skeleton's encoding.
func read(path string) (string, encoding, error) {
	b, err := readRaw(path)
	if err != nil {
		return "", latin1, err
	}

	return decodeFile(b)
}

// readRaw returns the file's bytes; a missing file has none.
func readRaw(path string) ([]byte, error) {
	b, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}

	return b, err
}
