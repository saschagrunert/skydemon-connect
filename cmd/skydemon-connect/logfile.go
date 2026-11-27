package main

import (
	"errors"
	"fmt"
	"io"
	"io/fs"
	"log"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"
)

const (
	logStem     = "SkyDemonConnect"
	logName     = logStem + ".log"
	previousLog = "SkyDemonConnect.previous.log"
	logDirMode  = 0o750
	logFileMode = 0o640
	// logFlags are the log's line prefix: date and time.
	logFlags = log.Ldate | log.Ltime
	// oldLogAge is when a per-process log is cleaned up.
	oldLogAge = 7 * 24 * time.Hour
)

// openLog starts a fresh log in the first of dirs it can write to, keeping
// the previous run's there as previousLog, and returns it; nil without
// any, and then logging is discarded rather than stopping the app.
func openLog(dirs ...string) *os.File {
	for _, d := range dirs {
		_ = os.MkdirAll(d, logDirMode)

		f, err := createLog(d)
		if err == nil {
			removeOldLogs(d, time.Now().Add(-oldLogAge))

			return f
		}
	}

	return nil
}

// createLog creates the log in dir. The previous log is moved aside and a
// new one created exclusively. If either fails because another copy, e.g.
// in another Windows session, has the log open or just created it, its log
// is left alone and this one gets a log of its own, named after its PID.
// One of those may be left from an exited process whose PID was reused;
// no running one shares ours, so it is overwritten.
func createLog(dir string) (*os.File, error) { return createLogWith(dir, os.Rename) }

// createLogWith is createLog with rename, replaced in tests.
func createLogWith(dir string, rename func(from, to string) error) (*os.File, error) {
	const (
		create = os.O_CREATE | os.O_WRONLY | os.O_EXCL
		reuse  = os.O_CREATE | os.O_WRONLY | os.O_TRUNC
	)

	path := filepath.Join(dir, logName)

	err := rename(path, filepath.Join(dir, previousLog))
	if err == nil || errors.Is(err, fs.ErrNotExist) {
		f, err := os.OpenFile(path, create, logFileMode)
		if !errors.Is(err, fs.ErrExist) {
			return f, err
		}
	}

	return os.OpenFile(ownLog(dir), reuse, logFileMode)
}

// ownLog is the path of this process's own log in dir, which removeOldLogs
// deletes after a week.
func ownLog(dir string) string {
	return filepath.Join(dir, fmt.Sprintf("%s.%d.log", logStem, os.Getpid()))
}

// openOwnLog opens this process's own log in the first of dirs it can
// write to, for a start that mustn't touch the main log; nil without any.
func openOwnLog(dirs ...string) *os.File {
	for _, d := range dirs {
		_ = os.MkdirAll(d, logDirMode)

		f, err := os.OpenFile(ownLog(d), os.O_CREATE|os.O_WRONLY|os.O_TRUNC, logFileMode)
		if err == nil {
			return f
		}
	}

	return nil
}

// newLogger returns a logger writing to f, which openLog opened, or one
// that discards its output when f is nil.
func newLogger(f *os.File) *log.Logger {
	if f == nil {
		return log.New(io.Discard, "", logFlags)
	}

	return log.New(f, "", logFlags)
}

// removeOldLogs deletes the per-process logs in dir, which a busy log
// leads to, that weren't written since before.
func removeOldLogs(dir string, before time.Time) {
	old, _ := filepath.Glob(filepath.Join(dir, logStem+".*.log"))

	for _, p := range old {
		// Only SkyDemonConnect.<pid>.log, not e.g. a log kept for a bug
		// report under another name.
		pid := strings.TrimSuffix(strings.TrimPrefix(filepath.Base(p), logStem+"."), ".log")

		_, err := strconv.Atoi(pid)
		if err != nil {
			continue
		}

		info, err := os.Stat(p)
		if err == nil && info.ModTime().Before(before) {
			_ = os.Remove(p)
		}
	}
}
