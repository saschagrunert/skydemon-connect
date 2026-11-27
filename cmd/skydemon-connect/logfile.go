package main

import (
	"errors"
	"fmt"
	"io"
	"io/fs"
	"log"
	"os"
	"path/filepath"
	"time"
)

const (
	logName     = "SkyDemonConnect.log"
	previousLog = "SkyDemonConnect.previous.log"
	logDirMode  = 0o750
	logFileMode = 0o640
	// oldLogAge is when a per-process log is cleaned up.
	oldLogAge = 7 * 24 * time.Hour
)

// openLog starts a fresh log in the first of dirs it can write to, keeping
// the previous run's there as previousLog. Without any, logging is
// discarded rather than stopping the app. It returns the logger, the log
// file and its path, both nil and empty then.
func openLog(dirs ...string) (*log.Logger, *os.File, string) {
	const flags = log.Ldate | log.Ltime

	for _, d := range dirs {
		_ = os.MkdirAll(d, logDirMode)

		// The previous log is moved aside. If that fails because it is
		// open, another copy runs from this folder in another Windows
		// session: its log is left alone, and this one gets its own.
		path := filepath.Join(d, logName)
		create := os.O_CREATE | os.O_WRONLY | os.O_TRUNC

		err := os.Rename(path, filepath.Join(d, previousLog))
		if err != nil && !errors.Is(err, fs.ErrNotExist) {
			path = filepath.Join(d, fmt.Sprintf("SkyDemonConnect.%d.log", os.Getpid()))
			create |= os.O_EXCL
		}

		f, err := os.OpenFile(path, create, logFileMode)
		if err == nil {
			removeOldLogs(d, time.Now().Add(-oldLogAge))

			return log.New(f, "", flags), f, path
		}
	}

	return log.New(io.Discard, "", flags), nil, ""
}

// removeOldLogs deletes the per-process logs in dir, which a busy log
// leads to, that weren't written since before.
func removeOldLogs(dir string, before time.Time) {
	old, _ := filepath.Glob(filepath.Join(dir, "SkyDemonConnect.*.log"))

	for _, p := range old {
		if p == filepath.Join(dir, previousLog) {
			continue
		}

		info, err := os.Stat(p)
		if err == nil && info.ModTime().Before(before) {
			_ = os.Remove(p)
		}
	}
}
