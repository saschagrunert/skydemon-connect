// Package main is SkyDemonConnect: it feeds the Microsoft Flight Simulator
// 2024 aircraft position to SkyDemon (and other EFB apps) at 30 Hz, via
// ForeFlight-style UDP and/or NMEA on a COM port.
package main

import (
	"bytes"
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"log"
	"math"
	"net"
	"net/netip"
	"os"
	"slices"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/saschagrunert/skydemon-connect/internal/demo"
	"github.com/saschagrunert/skydemon-connect/internal/feed"
	"github.com/saschagrunert/skydemon-connect/internal/foreflight"
	"github.com/saschagrunert/skydemon-connect/internal/serial"
	"github.com/saschagrunert/skydemon-connect/internal/simconnect"
	"github.com/saschagrunert/skydemon-connect/internal/targets"
)

// version is set at build time with -ldflags "-X main.version=...".
var version = "dev"

const (
	defaultBaud    = 115200
	defaultDemoLat = 51.4239 // Leipzig/Halle (EDDP)
	defaultDemoLon = 12.2364
	// The demo circle's longitude scale is 1/cos(lat), which grows without
	// bound towards the poles.
	maxDemoLat   = 85.0
	maxLon       = 180.0
	shutdownWait = 2 * time.Second
	// How often "auto" UDP targets follow network changes (Wi-Fi
	// reconnects, a laptop changing networks).
	targetRefresh = 10 * time.Second

	// maxName keeps XGPS/XATT messages short, as the ForeFlight
	// specification asks.
	maxName = 32

	exitError = 1
	exitUsage = 2
)

var (
	errNoOut = errors.New("nothing to do: set -udp and/or -com")
	errArg   = errors.New("options start with a dash, e.g. -demo; -udp takes one" +
		" comma-separated list without spaces (in PowerShell, put it in quotes)")
	errName    = fmt.Errorf("-name must be 1 to %d printable ASCII characters, no commas", maxName)
	errDemoLat = fmt.Errorf("-demo-lat must be within ±%g", maxDemoLat)
	errDemoLon = fmt.Errorf("-demo-lon must be within ±%g", maxLon)
)

type config struct {
	// spec is -udp, parsed.
	spec *targets.Spec
	// passOn are the options "Start with MSFS" writes to exe.xml, so that
	// MSFS starts the app as it was started now, quoted for a command line.
	passOn           []string
	udp, name, com   string
	baud             int
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
		if !localFlag(f.Name) {
			c.passOn = append(c.passOn, quoteArg("-"+f.Name+"="+f.Value.String()))
		}
	})

	return c, "", nil
}

// quoteArg quotes s for a Windows command line, as CommandLineToArgvW and
// Go's flag parsing read it back.
func quoteArg(s string) string {
	if s != "" && !strings.ContainsAny(s, " \t\"") {
		return s
	}

	var b strings.Builder

	b.WriteByte('"')

	slashes := 0

	for _, r := range s {
		switch r {
		case '\\':
			slashes++
		case '"':
			b.WriteString(strings.Repeat(`\`, slashes+1))
			slashes = 0
		default:
			slashes = 0
		}

		b.WriteRune(r)
	}

	b.WriteString(strings.Repeat(`\`, slashes)) // before the closing quote
	b.WriteByte('"')

	return b.String()
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

	_, err = serial.CheckBaud(c.baud)
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

// outputs are the opened destinations.
type outputs struct {
	feed.Outputs

	resolver *targets.Resolver
	targets  targets.List
	closers  []io.Closer
}

// Close closes everything opened.
func (o *outputs) Close() {
	for _, c := range o.closers {
		_ = c.Close()
	}
}

// openOutputs opens what c, as validated by parseFlags, sends to.
func openOutputs(ctx context.Context, c *config, l *log.Logger) (*outputs, error) {
	spec := c.spec
	outs := &outputs{resolver: spec.Resolver(l)}
	outs.Name, outs.NoAttitude, outs.SerialName = c.name, c.noAttitude, c.com

	if !spec.Empty() {
		// Go enables SO_BROADCAST on IPv4 UDP sockets.
		conn, err := net.ListenUDP("udp4", nil)
		if err != nil {
			return nil, fmt.Errorf("udp: %w", err)
		}

		outs.closers = append(outs.closers, conn)
		outs.UDP, outs.targets = conn, outs.resolver.Resolve(ctx)

		if spec.Dynamic() {
			l.Printf("udp: %s, sending to %s", spec.Describe(), outs.targets)
		} else {
			l.Printf("udp: sending to %s", outs.targets)
		}
	}

	if c.com != "" {
		// Only an invalid baud fails here, which parseFlags rejects; a
		// missing port is opened once it is there.
		port, err := serial.Open(c.com, c.baud)
		if err != nil {
			outs.Close()

			return nil, fmt.Errorf("COM port: %w", err)
		}

		outs.Serial, outs.closers = port, append(outs.closers, port)

		l.Printf("nmea: RMC/GGA/GSA to %s at %d baud", c.com, c.baud)
	}

	return outs, nil
}

// running is a started feed.
type running struct {
	feed *feed.Feed
	// quit is closed when the app should exit: once parent is done, or
	// once a SimConnect session ended, with endedQuits.
	quit <-chan struct{}
	// stop ends the background work, waits briefly for it, so that the
	// SimConnect session closes cleanly, and closes the outputs.
	stop func()
}

// simFunc runs the simulator connection: simconnect.Run, or a fake in
// tests.
type simFunc func(ctx context.Context, l *log.Logger, h simconnect.Handlers)

// input is where positions come from.
type input struct {
	sim simFunc
	// endedQuits: the app exits when a SimConnect session ends, i.e. MSFS
	// closed. With -auto, unless the MSFS process itself is watched, which
	// also covers MSFS closing before a session ever started.
	endedQuits bool
}

// endedQuits reports whether the app exits when a SimConnect session ends:
// with -auto, unless the MSFS process itself is watched, which also covers
// MSFS closing before a session started, and doesn't end the app when
// only the connection drops.
func endedQuits(auto, msfsWatched bool) bool { return auto && !msfsWatched }

// startFeed opens the outputs and runs the feed, its input (sim, or the
// demo with -demo) and the target refresh in the background, logging to l.
func startFeed(parent context.Context, c *config, src input, l *log.Logger) (*running, error) {
	outs, err := openOutputs(parent, c, l)
	if err != nil {
		return nil, err
	}

	f := feed.New(outs.Outputs, l)
	f.SetTargets(outs.targets)

	ctx, cancel := context.WithCancel(parent)
	// quit is done when parent is, or when the feed asks to quit.
	quit, requestQuit := context.WithCancel(parent)

	var wg sync.WaitGroup

	wg.Go(func() { f.Run(ctx) })
	wg.Go(func() { runInput(ctx, f, c, src, requestQuit, l) })

	if c.spec.Dynamic() {
		wg.Go(func() {
			refreshTargets(ctx, targetRefresh, outs.resolver.Resolve, f.SetTargets, outs.targets, l)
		})
	}

	// stop may run twice: deferred, and at logoff from the tray.
	stop := sync.OnceFunc(func() {
		cancel()
		requestQuit()

		done := make(chan struct{})

		go func() {
			wg.Wait()
			close(done)
		}()

		select {
		case <-done:
		case <-time.After(shutdownWait):
			l.Printf("shutdown: gave up waiting for background work")
		}

		outs.Close()
	})

	return &running{feed: f, quit: quit.Done(), stop: stop}, nil
}

func runInput(
	ctx context.Context,
	f *feed.Feed,
	c *config,
	src input,
	quit func(),
	l *log.Logger,
) {
	if c.demo {
		l.Printf("demo: circling %.4f, %.4f", c.demoLat, c.demoLon)
		f.SetLive(true)
		demo.Run(ctx, c.demoLat, c.demoLon, f.Update)

		return
	}

	src.sim(ctx, l, simconnect.Handlers{
		Update: f.Update,
		Live:   f.SetLive,
		Ended: func() {
			if src.endedQuits {
				l.Printf("auto: MSFS closed, exiting")
				quit()
			}
		},
		Failed: f.SetError,
	})
}

// refreshTargets calls set with the result of resolve each time it changes,
// checking every interval.
func refreshTargets(
	ctx context.Context,
	every time.Duration,
	resolve func(context.Context) targets.List,
	set func([]netip.AddrPort),
	current []netip.AddrPort,
	l *log.Logger,
) {
	tick := time.NewTicker(every)
	defer tick.Stop()

	for {
		select {
		case <-ctx.Done():
			return
		case <-tick.C:
			next := resolve(ctx)
			if ctx.Err() != nil {
				return // shutting down: lookups were cut short
			}

			if !slices.Equal(next, current) {
				l.Printf("udp: network changed, sending to %s", next)
				set(next)
				current = next
			}
		}
	}
}

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
