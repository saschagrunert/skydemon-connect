// Package main is SkyDemonConnect: it feeds the Microsoft Flight Simulator
// 2024 aircraft position to SkyDemon (and other EFB apps) at 30 Hz, via
// ForeFlight-style UDP and/or NMEA on a COM port.
package main

import (
	"errors"
	"flag"
	"os"
)

// version is set at build time with -ldflags "-X main.version=...".
var version = "dev"

const (
	exitError = 1
	exitUsage = 2
)

func main() {
	c, usage, err := parseFlags(os.Args[1:])

	switch {
	case errors.Is(err, flag.ErrHelp):
		notify(usage, false)
		os.Exit(0)
	case err != nil:
		notify(usage, true)
		os.Exit(exitUsage)
	case c.version:
		notify("SkyDemonConnect "+version, false)
		os.Exit(0)
	}

	os.Exit(run(&c))
}
