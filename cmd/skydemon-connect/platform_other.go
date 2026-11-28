//go:build !windows

package main

import (
	"context"
	"fmt"
	"log"
	"os"
	"path/filepath"
)

// prepare has nothing to do outside Windows.
func prepare() {}

// Outside Windows there is no simulator; the console mode is useful with
// -demo for testing.
func run(c *config) int {
	err := runConsoleUntilInterrupt(c, os.Stdout)
	if err != nil {
		notify(err.Error(), true)

		return exitError
	}

	return 0
}

// onSessionEnd has no session end to wait for outside Windows: SIGTERM
// covers it.
func onSessionEnd(func()) {}

// watchMSFS has no MSFS to watch outside Windows.
func watchMSFS(ctx context.Context, _ *log.Logger) (context.Context, bool) { return ctx, false }

func notify(text string, isErr bool) {
	out := os.Stdout
	if isErr {
		out = os.Stderr
	}

	_, _ = fmt.Fprintln(out, text)
}

// documentsDir is the Documents folder in the home folder.
func documentsDir() (string, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return "", fmt.Errorf("no Documents folder: %w", err)
	}

	return filepath.Join(home, "Documents"), nil
}

// msfsRunning: MSFS runs on Windows only.
func msfsRunning() bool { return false }
