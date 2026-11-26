package main

import (
	"io"
	"log"
	"os"
	"path/filepath"
)

const (
	logName     = "SkyDemonConnect.log"
	previousLog = "SkyDemonConnect.previous.log"
	logDirMode  = 0o750
)

// openLog starts a fresh log in the first of dirs it can write to, keeping
// the previous run's there as previousLog. Without any, logging is
// discarded rather than stopping the app. It returns the logger, the log
// file and its path, both nil and empty then.
func openLog(dirs ...string) (*log.Logger, *os.File, string) {
	const flags = log.Ldate | log.Ltime

	for _, d := range dirs {
		path := filepath.Join(d, logName)

		_ = os.MkdirAll(d, logDirMode)
		_ = os.Rename(path, filepath.Join(d, previousLog))

		f, err := os.Create(path)
		if err == nil {
			return log.New(f, "", flags), f, path
		}
	}

	return log.New(io.Discard, "", flags), nil, ""
}
