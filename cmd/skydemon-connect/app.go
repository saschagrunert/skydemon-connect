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
	// How long the end waits for the routes, whose plugin may be
	// updated once MSFS is gone: longer than the rest, as a package cut
	// off halfway is missing at MSFS's next start.
	routesWait = 10 * time.Second
	// How often "auto" UDP targets and host names follow network changes
	// (Wi-Fi reconnects, a laptop changing networks, a tablet's new
	// address).
	targetRefresh = 10 * time.Second
)

// outputs are the opened destinations.
type outputs struct {
	// Outputs is what the feed sends to. UDP and Serial are set only when
	// open, and are closed by shutdown.
	feed.Outputs

	resolver *targets.Resolver
	// targets are the UDP destinations at the start: the ones that need no
	// name lookup, so that nothing waits for a tablet that is off.
	targets targets.List
}

// nmeaText describes the NMEA output, e.g. "COM11 at 115200 baud".
func nmeaText(c *config) string { return fmt.Sprintf("%s at %d baud", c.com, c.baud) }

// shutdown closes the outputs once the background work, already
// told to stop, ended or shutdownWait passed.
func (o *outputs) shutdown(work *sync.WaitGroup, l *log.Logger) {
	// Closing the COM port first ends a write blocked in the driver,
	// which the wait below would otherwise sit out. An open that blocks
	// in the driver, e.g. of a Bluetooth port whose device is off, is
	// still waited out.
	closeQuietly(o.Serial)

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

	closeQuietly(o.UDP)
}

// waitFor waits until work is done, at most for wait.
func waitFor(work *sync.WaitGroup, wait time.Duration, what string, l *log.Logger) {
	done := make(chan struct{})

	go func() {
		work.Wait()
		close(done)
	}()

	select {
	case <-done:
	case <-time.After(wait):
		l.Printf("shutdown: gave up waiting for the %s", what)
	}
}

// closeQuietly closes w if it is open; at shutdown there is nobody to tell
// about an error.
func closeQuietly(w any) {
	if c, ok := w.(io.Closer); ok {
		_ = c.Close()
	}
}

// openOutputs opens what c, as validated by parseFlags, sends to.
func openOutputs(c *config, l *log.Logger) (*outputs, error) {
	spec := c.spec
	outs := &outputs{
		UDP:        nil,
		Name:       c.name,
		NoAttitude: c.noAttitude,
		Serial:     nil,
		SerialName: c.com,
		resolver:   spec.Resolver(l),
		targets:    nil,
	}

	if !spec.Empty() {
		// Go enables SO_BROADCAST on IPv4 UDP sockets.
		conn, err := net.ListenUDP("udp4", nil)
		if err != nil {
			return nil, fmt.Errorf("udp: %w", err)
		}

		outs.UDP, outs.targets = conn, outs.resolver.Known()

		l.Printf("udp: %s, sending to %s", spec.Describe(), outs.targets)
	}

	if c.com != "" {
		// A missing port is opened once it is there.
		outs.Serial = serial.Open(c.com, c.baud)

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
	// routes hands SkyDemon's routes to the G1000 until its context is
	// done; nil leaves them alone.
	routes routesFunc
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

	f := feed.New(outs.Outputs, l)
	f.SetTargets(outs.targets)

	if c.testTraffic {
		l.Printf("test traffic: TEST1 to TEST3 around the own aircraft")
		f.SetTrafficFunc(demo.NewTestTraffic(time.Now()).At)
	}

	ctx, cancel := context.WithCancel(parent)
	// quit is done when parent is, or when the feed asks to quit.
	quit, requestQuit := context.WithCancel(parent)

	var wg, routes sync.WaitGroup

	wg.Go(func() { f.Run(ctx) })
	wg.Go(func() { runInput(ctx, f, c, src, requestQuit, l) })

	if src.routes != nil && !c.noRoute {
		routes.Go(func() { src.routes(ctx, c, l) })
	}

	if c.spec.Dynamic() {
		wg.Go(func() {
			outs.resolver.Refresh(ctx, targetRefresh, outs.targets, f.SetTargets)
		})
	}

	// stop may run twice: deferred, and at logoff from the tray.
	stop := sync.OnceFunc(func() {
		cancel()
		requestQuit()
		outs.shutdown(&wg, l)
		waitFor(&routes, routesWait, "routes", l)
	})

	destinations := func() []string {
		var lines []string

		if outs.UDP != nil {
			// Short, for the menu: without the default port.
			t := targets.List(f.Targets()).Format(foreflight.Port)
			if t == "" {
				t = "no target yet"
			}

			lines = append(lines, "UDP: "+t)
		}

		if c.com != "" {
			lines = append(lines, "NMEA: "+nmeaText(c))
		}

		return lines
	}

	return &running{feed: f, quit: quit.Done(), stop: stop, destinations: destinations}, nil
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

	h := simconnect.Handlers{
		Update:    f.Update,
		Live:      f.SetLive,
		Connected: f.SetConnected,
		Ended: func() {
			if src.quitOnSessionEnd {
				l.Printf("auto: MSFS closed, exiting")
				quit()
			}
		},
		Failed:  f.SetError,
		Traffic: nil,
	}

	// -test-traffic replaces the sim's traffic, which only goes over UDP.
	if !c.noTraffic && !c.testTraffic && !c.spec.Empty() {
		h.Traffic = f.SetTraffic
	}

	src.sim(ctx, l, h)
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
