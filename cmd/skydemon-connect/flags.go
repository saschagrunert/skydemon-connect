package main

import (
	"errors"
	"flag"
	"fmt"
	"io"
	"math"
	"runtime"
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
	errQuiet     = errors.New("-quiet only works with -console")
	errBaudNoCom = errors.New("-baud only works with -com")
	errName      = fmt.Errorf(
		"-name must be 1 to %d printable ASCII characters, no commas",
		maxName,
	)
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

// parseFlags parses args. On -help it also returns the options' help, on
// an error the error and how to see that help.
func parseFlags(args []string) (config, string, error) {
	var c config

	fs := flag.NewFlagSet("SkyDemonConnect", flag.ContinueOnError)
	fs.SetOutput(io.Discard) // the errors are returned, the help built below
	fs.Usage = func() {}

	// local marks the options that only make sense for this start, not for
	// MSFS's: "Start with MSFS" doesn't pass them on.
	local := map[string]bool{}
	localFlag := func(name string) string {
		local[name] = true

		return name
	}

	fs.StringVar(&c.udp, "udp", targets.Auto,
		"comma-separated UDP targets (host:port) for XGPS/XATT; -udp= disables UDP (with -com). "+
			`"auto" is this PC and every local network (Wi-Fi tablets), port `+
			strconv.Itoa(foreflight.Port))
	fs.BoolVar(&c.noAttitude, "no-xatt", false, "don't send XATT attitude messages")
	fs.StringVar(&c.name, "name", "MSFS",
		"simulator name in XGPS/XATT messages, up to "+strconv.Itoa(maxName)+
			" ASCII characters, no commas")
	fs.StringVar(&c.com, "com", "", "COM port for NMEA output, e.g. COM11")
	fs.IntVar(&c.baud, "baud", defaultBaud, "COM port baud rate (with -com)")
	fs.BoolVar(&c.demo, localFlag("demo"), false,
		"fly a 100 kt circle without MSFS, to test SkyDemon")
	fs.Float64Var(&c.demoLat, localFlag("demo-lat"), defaultDemoLat, "demo circle centre latitude")
	fs.Float64Var(&c.demoLon, localFlag("demo-lon"), defaultDemoLon, "demo circle centre longitude")
	fs.BoolVar(&c.quiet, localFlag("quiet"), false, "with -console: no status line")
	fs.BoolVar(&c.console, localFlag("console"), false, "show a console instead of the tray icon")
	fs.BoolVar(&c.auto, localFlag("auto"), false,
		`started by MSFS (exe.xml): no welcome balloon or "already running" dialog,`+
			" exit when MSFS closes")
	fs.BoolVar(&c.version, localFlag("version"), false, "show the version and exit")

	err := fs.Parse(args)
	if errors.Is(err, flag.ErrHelp) {
		return c, help(fs), err
	}

	set := map[string]bool{}

	fs.Visit(func(f *flag.Flag) { set[f.Name] = true })

	if err == nil {
		err = validate(&c, fs.Args(), set)
	}

	if err != nil {
		return c, err.Error() + "\n\nRun with -help to see the options.", fmt.Errorf(
			"parse flags: %w",
			err,
		)
	}

	fs.Visit(func(f *flag.Flag) {
		// A flag given at its default changes nothing: leaving it out
		// keeps exe.xml matching a plain start.
		if !local[f.Name] && f.Value.String() != f.DefValue {
			c.passOn = append(c.passOn, "-"+f.Name+"="+f.Value.String())
		}
	})

	return c, "", nil
}

// help lists the options for -help, readable in a message box too: flag's
// own layout aligns with tabs, which a dialog's font doesn't.
func help(fs *flag.FlagSet) string {
	var b strings.Builder

	b.WriteString("SkyDemonConnect options:\n")

	fs.VisitAll(func(f *flag.Flag) {
		b.WriteString("\n-" + f.Name)

		if f.DefValue != "" && f.DefValue != "false" {
			b.WriteString(" (default " + f.DefValue + ")")
		}

		b.WriteString(":\n    " + f.Usage + "\n")
	})

	return b.String()
}

// validate checks the options; set holds the names of those given.
func validate(c *config, rest []string, set map[string]bool) error {
	switch {
	case len(rest) > 0:
		return fmt.Errorf("unexpected argument %q: %w", rest[0], errArg)
	case c.quiet && !c.console && runtime.GOOS == "windows":
		return errQuiet
	case !validName(c.name):
		return errName
	case math.IsNaN(c.demoLat) || math.Abs(c.demoLat) > maxDemoLat:
		return errDemoLat
	case math.IsNaN(c.demoLon) || math.Abs(c.demoLon) > maxLon:
		return errDemoLon
	}

	return validateOutputs(c, set["baud"])
}

// validateOutputs checks -udp, -com and -baud, and keeps -udp parsed;
// baudSet is whether -baud was given.
func validateOutputs(c *config, baudSet bool) error {
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

	if baudSet && c.com == "" {
		return errBaudNoCom
	}

	return nil
}

// validName reports whether name fits the XGPS/XATT messages: printable
// ASCII without the field separator.
func validName(name string) bool {
	return name != "" && len(name) <= maxName &&
		!strings.ContainsFunc(name, func(r rune) bool { return r == ',' || r < ' ' || r > '~' })
}
