package main

import (
	"context"
	"log"
	"path/filepath"
	"sync"
	"time"

	"github.com/saschagrunert/skydemon-connect/internal/exexml"
	"github.com/saschagrunert/skydemon-connect/internal/plugin"
	"github.com/saschagrunert/skydemon-connect/internal/routeserver"
)

// routesFunc hands SkyDemon's routes to the G1000 until ctx is done.
type routesFunc func(ctx context.Context, c *config, l *log.Logger)

const (
	// pluginRetry is how often an update of the plugin that waits for MSFS
	// to close, or that failed, is tried again, at most pluginTries times
	// once MSFS is closed.
	pluginRetry = 30 * time.Second
	pluginTries = 10
	// msfsExitWait is how long the end of an -auto start waits for MSFS to
	// be gone, to update the plugin: an app not watching MSFS quits with
	// the session, while MSFS is still closing. Less than routesWait.
	msfsExitWait = 8 * time.Second
	msfsExitPoll = 250 * time.Millisecond
)

// routeEnv is what runRoutes works with, other than in tests.
type routeEnv struct {
	installs exexml.Installs
	running  func() bool
	// addr is where the route server listens.
	addr string
	// retry is pluginRetry, exitWait msfsExitWait or, without -auto, 0.
	retry, exitWait time.Duration
}

// runRoutes brings the plugin up to date in each MSFS 2024 installation
// and serves the route last saved in SkyDemon to it.
func runRoutes(ctx context.Context, c *config, l *log.Logger) {
	env := routeEnv{
		installs: exexml.Find(),
		running:  msfsRunning,
		addr:     routeserver.Addr,
		retry:    pluginRetry,
		exitWait: 0,
	}

	// Quit chosen in the tray isn't kept waiting.
	if c.auto {
		env.exitWait = msfsExitWait
	}

	runRoutesWith(ctx, c, l, env)
}

func runRoutesWith(ctx context.Context, c *config, l *log.Logger, env routeEnv) {
	var wg sync.WaitGroup

	if installPlugin(env.installs, l, !env.running()) {
		wg.Go(func() { updatePluginLater(ctx, l, env) })
	}

	defer wg.Wait()

	dir := c.routes
	if dir == "" {
		docs, err := documentsDir()
		if err != nil {
			l.Printf("route: %v; set -routes to SkyDemon's Routes folder", err)

			return
		}

		dir = filepath.Join(docs, "SkyDemon", "Routes")
	}

	l.Printf("route: routes saved in %s go to the G1000", dir)

	err := routeserver.New(dir, plugin.ID(), l).RunOn(ctx, env.addr)
	if err != nil {
		// Another copy running, most likely, which serves them already.
		l.Printf("route: %v", err)
	}
}

// updatePluginLater updates the plugin once MSFS is closed, at the latest
// when ctx is done, as with -auto when MSFS exits, and tries again when
// that fails, e.g. with files MSFS held just after it closed.
func updatePluginLater(ctx context.Context, l *log.Logger, env routeEnv) {
	tick := time.NewTicker(env.retry)
	defer tick.Stop()

	for tries := 0; tries < pluginTries; {
		select {
		case <-ctx.Done():
			if gone(env.running, env.exitWait, msfsExitPoll) {
				installPlugin(env.installs, l, true)
			}

			return
		case <-tick.C:
			if env.running() {
				continue
			}

			tries++

			if !installPlugin(env.installs, l, true) {
				return
			}
		}
	}

	l.Printf("route: MSFS plugin not updated after %d tries; trying at the next start",
		pluginTries)
}

// gone reports whether MSFS isn't running, waiting at most wait for it to
// close.
func gone(running func() bool, wait, every time.Duration) bool {
	deadline := time.Now().Add(wait)

	for running() {
		if !time.Now().Before(deadline) {
			return false
		}

		time.Sleep(every)
	}

	return true
}

// installPlugin installs or, with update, updates the plugin next to
// each of the exe.xml files, i.e. for each MSFS 2024 installation. It
// reports whether an update was left for later, or failed.
func installPlugin(installs exexml.Installs, l *log.Logger, update bool) bool {
	dirs := make([]string, 0, len(installs))
	for _, exeXML := range installs {
		dirs = append(dirs, filepath.Dir(exeXML))
	}

	later := false

	for _, r := range plugin.Install(dirs, version, update) {
		switch {
		case r.Err != nil:
			// A write that failed, not a package refused: try again.
			later = later || r.Changed

			l.Printf("route: MSFS plugin: %v", r.Err)
		case r.Outdated:
			later = true

			l.Printf("route: MSFS plugin in %s is updated once MSFS is closed; "+
				"MSFS loads the new one at its next start", r.Community)
		case r.Changed:
			l.Printf("route: MSFS plugin installed in %s; MSFS loads it at its next start",
				r.Community)
		default:
			l.Printf("route: MSFS plugin up to date in %s", r.Community)
		}
	}

	return later
}
