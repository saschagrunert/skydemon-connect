package main

import (
	"context"
	"fmt"
	"io"
	"log"
	"net"
	"net/netip"
	"slices"
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

const (
	shutdownWait = 2 * time.Second
	// How often "auto" UDP targets and host names follow network changes
	// (Wi-Fi reconnects, a laptop changing networks, a tablet's new
	// address).
	targetRefresh = 10 * time.Second
)

// outputs are the opened destinations.
type outputs struct {
	resolver *targets.Resolver
	// conn sends to the UDP targets; nil without any.
	conn *net.UDPConn
	// port is the COM port for NMEA; nil without -com.
	port io.WriteCloser
	// targets are the UDP destinations at the start: the ones that need no
	// name lookup, so that nothing waits for a tablet that is off.
	targets targets.List
}

// feedOutputs returns what the feed sends to, as c configures it.
func (o *outputs) feedOutputs(c *config) feed.Outputs {
	outs := feed.Outputs{
		UDP:        nil,
		Name:       c.name,
		NoAttitude: c.noAttitude,
		Serial:     nil,
		SerialName: c.com,
	}

	// An interface holding a nil pointer isn't nil: only set what is open.
	if o.conn != nil {
		outs.UDP = o.conn
	}

	if o.port != nil {
		outs.Serial = o.port
	}

	return outs
}

// nmeaText describes the NMEA output, e.g. "COM11 at 115200 baud".
func nmeaText(c *config) string { return fmt.Sprintf("%s at %d baud", c.com, c.baudRate) }

// shutdown closes the outputs once the background work, already
// told to stop, ended or shutdownWait passed.
func (o *outputs) shutdown(work *sync.WaitGroup, l *log.Logger) {
	// Closing the COM port first ends a write blocked in the driver,
	// which the wait below would otherwise sit out. An open that blocks
	// in the driver, e.g. of a Bluetooth port whose device is off, is
	// still waited out.
	if o.port != nil {
		_ = o.port.Close()
	}

	done := make(chan struct{})

	go func() {
		work.Wait()
		close(done)
	}()

	select {
	case <-done:
	case <-time.After(shutdownWait):
		l.Printf("shutdown: gave up waiting for background work")
	}

	if o.conn != nil {
		_ = o.conn.Close()
	}
}

// openOutputs opens what c, as validated by parseFlags, sends to.
func openOutputs(c *config, l *log.Logger) (*outputs, error) {
	spec := c.spec
	outs := &outputs{resolver: spec.Resolver(l), conn: nil, port: nil, targets: nil}

	if !spec.Empty() {
		// Go enables SO_BROADCAST on IPv4 UDP sockets.
		conn, err := net.ListenUDP("udp4", nil)
		if err != nil {
			return nil, fmt.Errorf("udp: %w", err)
		}

		outs.conn, outs.targets = conn, outs.resolver.Known()

		l.Printf("udp: %s, sending to %s", spec.Describe(), outs.targets)
	}

	if c.com != "" {
		// A missing port is opened once it is there.
		outs.port = serial.Open(c.com, c.baudRate)

		l.Printf("nmea: RMC/GGA/GSA to %s", nmeaText(c))
	}

	return outs, nil
}

// running is a started feed.
type running struct {
	feed *feed.Feed
	// quit is closed when the app should exit: once parent is done, or
	// once a SimConnect session ended, with quitOnSessionEnd.
	quit <-chan struct{}
	// stop ends the background work and closes the outputs: the COM port
	// at once, the UDP socket once the work ended, so that the SimConnect
	// session closes cleanly, or after shutdownWait.
	stop func()
	// destinations lists where positions go, for the tray's menu.
	destinations func() []string
}

// simFunc runs the simulator connection: simconnect.Run, or a fake in
// tests.
type simFunc func(ctx context.Context, l *log.Logger, h simconnect.Handlers)

// input is where positions come from.
type input struct {
	sim simFunc
	// quitOnSessionEnd: the app exits when a SimConnect session ends,
	// i.e. MSFS closed; see sessionEndQuits.
	quitOnSessionEnd bool
}

// sessionEndQuits reports whether the app exits when a SimConnect session
// ends: with -auto, unless the MSFS process itself is watched, which also
// covers MSFS closing before a session started, and doesn't end the app
// when only the connection drops.
func sessionEndQuits(auto, msfsWatched bool) bool { return auto && !msfsWatched }

// startFeed opens the outputs and runs the feed, its input (sim, or the
// demo with -demo) and the target refresh in the background, logging to l.
func startFeed(parent context.Context, c *config, src input, l *log.Logger) (*running, error) {
	outs, err := openOutputs(c, l)
	if err != nil {
		return nil, err
	}

	f := feed.New(outs.feedOutputs(c), l)
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
		outs.shutdown(&wg, l)
	})

	destinations := func() []string {
		var lines []string

		if outs.conn != nil {
			lines = append(lines, "UDP: "+menuTargets(f.Targets()))
		}

		if c.com != "" {
			lines = append(lines, "NMEA: "+nmeaText(c))
		}

		return lines
	}

	return &running{feed: f, quit: quit.Done(), stop: stop, destinations: destinations}, nil
}

// menuTargets lists targets for the menu, short: without the default
// port.
func menuTargets(t []netip.AddrPort) string {
	if len(t) == 0 {
		return "no target yet"
	}

	s := make([]string, len(t))
	for i, a := range t {
		s[i] = a.String()
		if a.Port() == foreflight.Port {
			s[i] = a.Addr().String()
		}
	}

	return strings.Join(s, ", ")
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
		Update:    f.Update,
		Live:      f.SetLive,
		Connected: f.SetConnected,
		Ended: func() {
			if src.quitOnSessionEnd {
				l.Printf("auto: MSFS closed, exiting")
				quit()
			}
		},
		Failed: f.SetError,
	})
}

// refreshTargets calls set with the result of resolve at once, for the
// host names left out at the start, and then each time it changes,
// checking every interval.
func refreshTargets(
	ctx context.Context,
	every time.Duration,
	resolve func(context.Context) targets.List,
	set func([]netip.AddrPort),
	current targets.List,
	l *log.Logger,
) {
	tick := time.NewTicker(every)
	defer tick.Stop()

	for {
		next := resolve(ctx)
		if ctx.Err() != nil {
			return // shutting down: lookups were cut short
		}

		if !slices.Equal(next, current) {
			l.Printf("udp: sending to %s", next)
			set(next)
			current = next
		}

		select {
		case <-ctx.Done():
			return
		case <-tick.C:
		}
	}
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
