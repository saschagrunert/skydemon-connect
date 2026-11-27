package main

import (
	"context"
	"fmt"
	"io"
	"log"
	"net"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
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
	feed.Outputs

	resolver *targets.Resolver
	// targets are the UDP destinations at the start: the ones that need no
	// name lookup, so that nothing waits for a tablet that is off.
	targets targets.List
	udp     io.Closer
	serial  io.Closer
}

// shutdown closes the outputs once the background work, already
// told to stop, ended or shutdownWait passed.
func (o *outputs) shutdown(work *sync.WaitGroup, l *log.Logger) {
	// Closing the COM port first ends a write blocked in the driver,
	// which the wait below would otherwise sit out. An open that blocks
	// in the driver, e.g. of a Bluetooth port whose device is off, is
	// still waited out.
	closeQuietly(o.serial)

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

	closeQuietly(o.udp)
}

// openOutputs opens what c, as validated by parseFlags, sends to.
func openOutputs(c *config, l *log.Logger) (*outputs, error) {
	spec := c.spec
	outs := &outputs{resolver: spec.Resolver(l)}
	outs.Name, outs.NoAttitude, outs.SerialName = c.name, c.noAttitude, c.com

	if !spec.Empty() {
		// Go enables SO_BROADCAST on IPv4 UDP sockets.
		conn, err := net.ListenUDP("udp4", nil)
		if err != nil {
			return nil, fmt.Errorf("udp: %w", err)
		}

		outs.UDP, outs.udp, outs.targets = conn, conn, outs.resolver.Known()

		l.Printf("udp: %s, sending to %s", spec.Describe(), outs.targets)
	}

	if c.com != "" {
		// A missing port is opened once it is there.
		port := serial.Open(c.com, c.baudRate)
		outs.Serial, outs.serial = port, port

		l.Printf("nmea: RMC/GGA/GSA to %s at %d baud", c.com, c.baudRate)
	}

	return outs, nil
}

// running is a started feed.
type running struct {
	feed *feed.Feed
	// quit is closed when the app should exit: once parent is done, or
	// once a SimConnect session ended, with endedQuits.
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
	// endedQuits: the app exits when a SimConnect session ends, i.e. MSFS
	// closed; see endedQuits.
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
	outs, err := openOutputs(c, l)
	if err != nil {
		return nil, err
	}

	f := feed.New(outs.Outputs, l)

	var current atomic.Pointer[targets.List]

	setTargets := func(t targets.List) {
		current.Store(&t)
		f.SetTargets(t)
	}

	setTargets(outs.targets)

	ctx, cancel := context.WithCancel(parent)
	// quit is done when parent is, or when the feed asks to quit.
	quit, requestQuit := context.WithCancel(parent)

	var wg sync.WaitGroup

	wg.Go(func() { f.Run(ctx) })
	wg.Go(func() { runInput(ctx, f, c, src, requestQuit, l) })

	if c.spec.Dynamic() {
		wg.Go(func() {
			refreshTargets(ctx, targetRefresh, outs.resolver.Resolve, setTargets, outs.targets, l)
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

		if t := current.Load(); t != nil && outs.udp != nil {
			lines = append(lines, "UDP: "+menuTargets(*t))
		}

		if c.com != "" {
			lines = append(lines, fmt.Sprintf("NMEA: %s at %d baud", c.com, c.baudRate))
		}

		return lines
	}

	return &running{feed: f, quit: quit.Done(), stop: stop, destinations: destinations}, nil
}

// menuTargets lists targets for the menu, short: without the default
// port.
func menuTargets(t targets.List) string {
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

// closeQuietly closes c, if any; at shutdown there is nobody to tell about
// an error.
func closeQuietly(c io.Closer) {
	if c != nil {
		_ = c.Close()
	}
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
			if src.endedQuits {
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
	set func(targets.List),
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
