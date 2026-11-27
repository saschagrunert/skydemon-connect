//go:build !windows

package main

import (
	"fmt"
	"os"
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

func notify(text string, isErr bool) {
	out := os.Stdout
	if isErr {
		out = os.Stderr
	}

	_, _ = fmt.Fprintln(out, text)
}
