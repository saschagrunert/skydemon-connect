package main

import (
	"context"
	"log"
	"path/filepath"
	"sync"
	"time"

	"github.com/saschagrunert/skydemon-connect/internal/exexml"
	"github.com/saschagrunert/skydemon-connect/internal/gtn"
	"github.com/saschagrunert/skydemon-connect/internal/plugin"
	"github.com/saschagrunert/skydemon-connect/internal/routeserver"
)

// routesFunc hands SkyDemon's routes to the avionics until ctx is done.
type routesFunc func(ctx context.Context, c *config, links routeLinks, l *log.Logger)

// routeLinks are what the routes need of the rest of the app.
type routeLinks struct {
	// gtn writes the route for the TDS GTNXi, nil without one.
	gtn *gtn.Writer
	// msfsClosed reports whether the app stops because MSFS closed, and
	// connected whether a session with MSFS is up.
	msfsClosed, connected func() bool
}

// routeCheck is how often a newly saved route is looked for, for the GTN.
const routeCheck = 2 * time.Second

// newGTN is the writer of flight plans for the TDS GTNXi installed below
// programData, nil without one.
func newGTN(programData string, l *log.Logger) *gtn.Writer {
	dir := gtn.Folder(programData)
	if dir == "" {
		return nil
	}

	l.Printf("route: routes go to the TDS GTNXi too, as %s in %s", gtn.Name, dir)

	return gtn.New(dir, l)
}

const (
	// pluginRetry is how often an update of the plugin that waits for MSFS
	// to close, or that failed, is tried again, at most pluginTries times
	// once MSFS is closed.
	pluginRetry = 30 * time.Second
	pluginTries = 10
	// msfsExitWait is how long the end, as MSFS closed, waits for MSFS to
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
	// retry is pluginRetry, exitWait msfsExitWait.
	retry, exitWait time.Duration
}

// runRoutes brings the plugin up to date in each MSFS 2024 installation,
// serves the route last saved in SkyDemon to it and writes it for the GTN.
func runRoutes(ctx context.Context, c *config, links routeLinks, l *log.Logger) {
	runRoutesWith(ctx, c, links, l, routeEnv{
		installs: exexml.Find(),
		running:  msfsRunning,
		addr:     routeserver.Addr,
		retry:    pluginRetry,
		exitWait: msfsExitWait,
	})
}

func runRoutesWith(
	ctx context.Context,
	c *config,
	links routeLinks,
	l *log.Logger,
	env routeEnv,
) {
	var wg sync.WaitGroup

	w := links.gtn
	if w != nil {
		defer w.Close() // after wg.Wait, deferred below: Watch writes no more
	}

	st := installPlugin(env.installs, l, !env.running())
	if st.later {
		wg.Go(func() { updatePluginLater(ctx, l, env, links.msfsClosed) })
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

	l.Printf("route: routes saved in %s go to %s",
		dir, routeTargets(len(env.installs) > 0, w != nil))

	// The ID tells the plugin MSFS runs to ask for a restart: not when
	// MSFS may run another package of that name, e.g. a linked one.
	id := plugin.ID()
	if st.foreign {
		id = ""
	}

	srv := routeserver.New(dir, id, l)

	if w != nil {
		wg.Go(func() { srv.Watch(ctx, routeCheck, links.connected, w.SetRoute) })
	}

	err := srv.RunOn(ctx, env.addr)
	if err != nil {
		// Another copy running, most likely, which serves them already.
		l.Printf("route: %v", err)
	}
}

// routeTargets names where routes go.
func routeTargets(toG1000, toGTN bool) string {
	switch {
	case toG1000 && toGTN:
		return "the G1000 and the GTN"
	case toGTN:
		return "the GTN"
	case toG1000:
		return "the G1000"
	default:
		return "no avionics yet: no MSFS 2024 nor TDS GTNXi found"
	}
}

// updatePluginLater updates the plugin once MSFS is closed, and tries
// again when that fails, e.g. with files MSFS held just after it closed.
// When ctx is done it updates if MSFS is closed, waiting for it only when
// the app stops because MSFS closed: a Quit, or Windows ending the
// session, isn't kept waiting.
func updatePluginLater(ctx context.Context, l *log.Logger, env routeEnv, msfsClosed func() bool) {
	tick := time.NewTicker(env.retry)
	defer tick.Stop()

	for tries := 0; tries < pluginTries; {
		select {
		case <-ctx.Done():
			wait := time.Duration(0)
			if msfsClosed() {
				wait = env.exitWait
			}

			if gone(env.running, wait, msfsExitPoll) {
				installPlugin(env.installs, l, true)
			} else {
				l.Printf("route: MSFS plugin is updated the next time SkyDemonConnect " +
					"runs and MSFS is closed or closes")
			}

			return
		case <-tick.C:
			if env.running() {
				continue
			}

			tries++

			if !installPlugin(env.installs, l, true).later {
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

// installState is what installPlugin found.
type installState struct {
	// later: an update was left for later, or failed. foreign: in each
	// installation found another package of the name, or a link, was left
	// alone, so that whatever MSFS runs isn't SkyDemonConnect's own.
	later, foreign bool
}

// installPlugin installs or, with update, updates the plugin next to
// each of the exe.xml files, i.e. for each MSFS 2024 installation.
func installPlugin(installs exexml.Installs, l *log.Logger, update bool) installState {
	dirs := make([]string, 0, len(installs))
	for _, exeXML := range installs {
		dirs = append(dirs, filepath.Dir(exeXML))
	}

	var (
		st             installState
		found, foreign int
	)

	for _, r := range plugin.Install(dirs, version, update) {
		// Found: what its Community folder holds is known.
		if r.Err == nil || r.Changed || r.Foreign {
			found++
		}

		if r.Foreign {
			foreign++
		}

		st.later = logInstall(r, l) || st.later
	}

	st.foreign = found > 0 && foreign == found

	return st
}

// logInstall logs what Install did in one installation, and reports
// whether there is more to do: an update left for later, or a write that
// failed, not a package refused.
func logInstall(r plugin.Result, l *log.Logger) bool {
	switch {
	case r.Err != nil:
		l.Printf("route: MSFS plugin: %v", r.Err)

		return r.Changed
	case r.Outdated:
		l.Printf("route: MSFS plugin in %s is updated once MSFS is closed; "+
			"MSFS loads the new one at its next start", r.Community)

		return true
	case r.Changed:
		l.Printf("route: MSFS plugin installed in %s; MSFS loads it at its next start",
			r.Community)
	default:
		l.Printf("route: MSFS plugin up to date in %s", r.Community)
	}

	return false
}
