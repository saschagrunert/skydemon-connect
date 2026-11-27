package main

import (
	"bytes"
	"errors"
	"flag"
	"fmt"
	"math"
	"slices"
	"strconv"
	"strings"

	"github.com/saschagrunert/skydemon-connect/internal/foreflight"
	"github.com/saschagrunert/skydemon-connect/internal/serial"
	"github.com/saschagrunert/skydemon-connect/internal/targets"
)

const (
	defaultBaud    = 115200
	defaultDemoLat = 51.4239 // Leipzig/Halle (EDDP)
	defaultDemoLon = 12.2364
	// The demo circle's longitude scale is 1/cos(lat), which grows without
	// bound towards the poles.
	maxDemoLat = 85.0
	maxLon     = 180.0

	// maxName keeps XGPS/XATT messages short, as the ForeFlight
	// specification asks.
	maxName = 32
)

var (
	errNoOut = errors.New("nothing to do: set -udp and/or -com")
	errArg   = errors.New("options start with a dash, e.g. -demo; -udp takes one" +
		" comma-separated list (in PowerShell, put it in quotes)")
	errName    = fmt.Errorf("-name must be 1 to %d printable ASCII characters, no commas", maxName)
	errDemoLat = fmt.Errorf("-demo-lat must be within ±%g", maxDemoLat)
	errDemoLon = fmt.Errorf("-demo-lon must be within ±%g", maxLon)
)

type config struct {
	// spec is -udp, parsed.
	spec *targets.Spec
	// passOn are the options "Start with MSFS" writes to exe.xml, so that
	// MSFS starts the app as it was started now, e.g. "-name=My Sim".
	passOn         []string
	udp, name, com string
	baud           int
	// baudRate is baud, checked.
	baudRate         uint32
	demoLat, demoLon float64
	noAttitude, demo bool
	quiet, console   bool
	auto             bool
	version          bool
}

// localFlag reports whether a flag only makes sense for this start, not
// for MSFS's.
func localFlag(name string) bool {
	switch name {
	case "auto", "console", "demo", "demo-lat", "demo-lon", "quiet", "version":
		return true
	}

	return false
}

// parseFlags parses args. On -help or an error it also returns the usage
// text, which starts with the error.
func parseFlags(args []string) (config, string, error) {
	var (
		c     config
		usage bytes.Buffer
	)

	fs := flag.NewFlagSet("SkyDemonConnect", flag.ContinueOnError)
	fs.SetOutput(&usage)

	fs.StringVar(&c.udp, "udp", targets.Auto,
		"comma-separated UDP targets (host:port) for XGPS/XATT; -udp= disables UDP (with -com).\n"+
			`"auto" is this PC and every local network (Wi-Fi tablets), port `+
			strconv.Itoa(foreflight.Port))
	fs.BoolVar(&c.noAttitude, "no-xatt", false, "don't send XATT attitude messages")
	fs.StringVar(&c.name, "name", "MSFS",
		"simulator name in XGPS/XATT messages, up to "+strconv.Itoa(maxName)+
			" ASCII characters, no commas")
	fs.StringVar(&c.com, "com", "", "COM port for NMEA output, e.g. COM11")
	fs.IntVar(&c.baud, "baud", defaultBaud, "COM port baud rate")
	fs.BoolVar(&c.demo, "demo", false, "fly a 100 kt circle without MSFS, to test SkyDemon")
	fs.Float64Var(&c.demoLat, "demo-lat", defaultDemoLat, "demo circle centre latitude")
	fs.Float64Var(&c.demoLon, "demo-lon", defaultDemoLon, "demo circle centre longitude")
	fs.BoolVar(&c.quiet, "quiet", false, "with -console: no status line")
	fs.BoolVar(&c.console, "console", false, "show a console instead of the tray icon")
	fs.BoolVar(&c.auto, "auto", false,
		`started by MSFS (exe.xml): no welcome balloon or "already running" dialog,`+
			" exit when MSFS closes")
	fs.BoolVar(&c.version, "version", false, "show the version and exit")

	err := fs.Parse(args)
	if err == nil {
		err = validate(&c, fs.Args())
		if err != nil {
			_, _ = fmt.Fprintln(&usage, err)

			fs.Usage()
		}
	}

	if err != nil {
		return c, usage.String(), fmt.Errorf("parse flags: %w", err)
	}

	fs.Visit(func(f *flag.Flag) {
		// A flag given at its default changes nothing: leaving it out
		// keeps exe.xml matching a plain start.
		if !localFlag(f.Name) && f.Value.String() != f.DefValue {
			c.passOn = append(c.passOn, "-"+f.Name+"="+f.Value.String())
		}
	})

	return c, "", nil
}

func validate(c *config, rest []string) error {
	switch {
	case len(rest) > 0:
		return fmt.Errorf("unexpected argument %q: %w", rest[0], errArg)
	case !validName(c.name):
		return errName
	case math.IsNaN(c.demoLat) || math.Abs(c.demoLat) > maxDemoLat:
		return errDemoLat
	case math.IsNaN(c.demoLon) || math.Abs(c.demoLon) > maxLon:
		return errDemoLon
	}

	return validateOutputs(c)
}

// validateOutputs checks -udp, -com and -baud, and keeps -udp parsed.
func validateOutputs(c *config) error {
	spec, err := targets.Parse(c.udp, foreflight.Port)
	if err != nil {
		return fmt.Errorf("-udp: %w", err)
	}

	if spec.Empty() && c.com == "" {
		return errNoOut
	}

	c.spec = spec

	c.baudRate, err = serial.CheckBaud(c.baud)
	if err != nil {
		return fmt.Errorf("-baud: %w", err)
	}

	return nil
}

// validName reports whether name fits the XGPS/XATT messages: printable
// ASCII without the field separator.
func validName(name string) bool {
	return name != "" && len(name) <= maxName &&
		!strings.ContainsFunc(name, func(r rune) bool { return r == ',' || r < ' ' || r > '~' })
}

// insideArchive reports whether dir is where an archiver runs an exe opened
// from inside an archive: Explorer's %TEMP%\Temp1_SkyDemonConnect.zip,
// 7-Zip's %TEMP%\7zO... or WinRAR's %TEMP%\Rar$EX....
func insideArchive(dir string) bool {
	return slices.ContainsFunc(
		strings.FieldsFunc(
			strings.ToLower(dir),
			func(r rune) bool { return r == '\\' || r == '/' },
		),
		func(part string) bool {
			return strings.HasSuffix(part, ".zip") || strings.HasPrefix(part, "7zo") ||
				strings.HasPrefix(part, "rar$ex")
		},
	)
}
