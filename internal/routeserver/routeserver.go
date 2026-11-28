// Package routeserver hands the route last saved in SkyDemon to the G1000
// plugin: it serves the newest .flightplan of SkyDemon's Routes folder as
// JSON, on this PC only.
package routeserver

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"hash/fnv"
	"io"
	"io/fs"
	"log"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/saschagrunert/skydemon-connect/internal/flightplan"
)

// Addr is where the plugin asks: the loopback only, never the network.
const Addr = "127.0.0.1:49080"

const (
	// maxFile is far more than any route: SkyDemon's are a few KiB.
	maxFile           = 1 << 20
	ext               = ".flightplan"
	readHeaderTimeout = 5 * time.Second
)

var errTooBig = errors.New("larger than 1 MiB, not a SkyDemon route")

// Point is a point of a route as the plugin takes it.
type Point struct {
	Lat       float64 `json:"lat"`
	Lon       float64 `json:"lon"`
	Aerodrome bool    `json:"aerodrome"`
}

// Route is a route as the plugin takes it.
type Route struct {
	// ID changes with the file's content: the plugin loads a route once.
	ID string `json:"id"`
	// Name is the file's name without .flightplan, as SkyDemon names
	// routes, e.g. "Padova - TrevisoS.Angelo".
	Name    string  `json:"name"`
	Points  []Point `json:"points"`
	LevelFt int     `json:"levelFt"`
	VFR     bool    `json:"vfr"`
}

// file is what the newest route was read from, to read it only once.
type file struct {
	mod  time.Time
	path string
	size int64
}

func (f file) same(o file) bool {
	return f.path == o.path && f.size == o.size && f.mod.Equal(o.mod)
}

// PluginHeader carries the ID of the plugin SkyDemonConnect installs: the
// plugin, which sends its own as the query parameter plugin, tells by it
// that MSFS still runs an older one.
const PluginHeader = "Skydemonconnect-Plugin"

// Server serves the newest route of a folder.
type Server struct {
	log *log.Logger
	dir string
	// host is the address requests must name: Addr, or where RunOn
	// listens. Set before serving, read only after.
	host string
	// plugin is the ID of the plugin installed.
	plugin string

	mu sync.Mutex
	// read is the file route, or err, is from.
	read  file
	route *Route
	err   error
	// served is the ID last served to the plugin, logged once.
	served string
	// refused are the origins logged as refused.
	refused map[string]bool
	// otherPlugin is the ID of another plugin logged last.
	otherPlugin string
	asked       bool
}

// New returns a Server for the routes in dir, for the plugin of the given
// ID.
func New(dir, plugin string, l *log.Logger) *Server {
	return &Server{
		log:     l,
		dir:     dir,
		host:    Addr,
		plugin:  plugin,
		mu:      sync.Mutex{},
		read:    file{mod: time.Time{}, path: "", size: 0},
		route:   nil,
		err:     nil,
		served:  "",
		refused: map[string]bool{},

		otherPlugin: "",
		asked:       false,
	}
}

// Run serves on Addr until ctx is done.
func (s *Server) Run(ctx context.Context) error { return s.RunOn(ctx, Addr) }

// RunOn serves on addr until ctx is done, e.g. on 127.0.0.1:0 in tests.
func (s *Server) RunOn(ctx context.Context, addr string) error {
	lc := net.ListenConfig{}

	listener, err := lc.Listen(ctx, "tcp", addr)
	if err != nil {
		return fmt.Errorf("listen on %s: %w", addr, err)
	}

	s.host = listener.Addr().String()

	srv := &http.Server{Handler: s, ReadHeaderTimeout: readHeaderTimeout}

	go func() {
		<-ctx.Done()

		_ = srv.Close()
	}()

	err = srv.Serve(listener)
	if err != nil && !errors.Is(err, http.ErrServerClosed) {
		return fmt.Errorf("serve: %w", err)
	}

	return nil
}

// ServeHTTP answers GET /route with the newest route, 404 without one.
func (s *Server) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	// A web page that makes the browser ask 127.0.0.1, or a name that
	// points there, gets nothing: the Host header gives it away.
	if r.Host != s.host {
		http.Error(w, "forbidden", http.StatusForbidden)

		return
	}

	if r.URL.Path != "/route" || r.Method != http.MethodGet {
		http.NotFound(w, r)

		return
	}

	s.allowOrigin(w, r.Header.Get("Origin"))
	w.Header().Set("Cache-Control", "no-store")

	if s.plugin != "" {
		w.Header().Set(PluginHeader, s.plugin)
	}

	// Only the plugin sends its ID: a browser or curl isn't the G1000.
	var route *Route

	if r.URL.Query().Has("plugin") {
		s.fromPlugin(r.URL.Query().Get("plugin"), r.Header.Get("Origin"))
		route = s.current()
	} else {
		route = s.newestRoute()
	}

	if route == nil {
		http.NotFound(w, r)

		return
	}

	w.Header().Set("Content-Type", "application/json")

	err := json.NewEncoder(w).Encode(route)
	if err != nil {
		s.log.Printf("route: send: %v", err)
	}
}

// Watch calls changed with the newest route whenever it changes, looking
// every interval while active says so, until ctx is done. With none left,
// what was handed on stays; with the newest deleted, the one saved before
// it is the newest, and handed on.
func (s *Server) Watch(
	ctx context.Context,
	every time.Duration,
	active func() bool,
	changed func(*Route),
) {
	tick := time.NewTicker(every)
	defer tick.Stop()

	last := ""

	for {
		// Inactive, e.g. with MSFS closed, the folder is left alone.
		if active() {
			if r := s.newestRoute(); r != nil && r.ID != last {
				last = r.ID
				changed(r)
			}
		}

		select {
		case <-ctx.Done():
			return
		case <-tick.C:
		}
	}
}

// allowOrigin lets the plugin, on a coui:// page of the sim, read the
// answer, and no web page in a browser. A refused origin is logged once:
// should the sim's ever change, the log tells.
func (s *Server) allowOrigin(w http.ResponseWriter, origin string) {
	w.Header().Set("Vary", "Origin")

	if strings.HasPrefix(origin, "coui://") {
		w.Header().Set("Access-Control-Allow-Origin", origin)
		w.Header().Set("Access-Control-Expose-Headers", PluginHeader)

		return
	}

	if origin == "" {
		return
	}

	s.mu.Lock()
	defer s.mu.Unlock()

	if !s.refused[origin] {
		s.refused[origin] = true
		s.log.Printf("route: refused a request from %q, not the sim", origin)
	}
}

// fromPlugin logs the plugin's first request, with its origin, which tells
// what the sim sends, and, once per ID, that MSFS runs another plugin than
// the one installed.
func (s *Server) fromPlugin(id, origin string) {
	s.mu.Lock()
	defer s.mu.Unlock()

	if !s.asked {
		s.asked = true
		s.log.Printf("route: the G1000 asked for the first time, from origin %q", origin)
	}

	// Without an ID, e.g. with a linked package, there is nothing to tell.
	if s.plugin != "" && id != s.plugin && id != s.otherPlugin {
		s.otherPlugin = id
		s.log.Printf("route: MSFS runs another plugin than the one installed; " +
			"restart MSFS to load it")
	}
}

// current is the newest route for the plugin, nil without one, and logs
// what changed.
func (s *Server) current() *Route {
	s.mu.Lock()
	defer s.mu.Unlock()

	route := s.newestLocked()
	if route != nil && route.ID != s.served {
		s.served = route.ID
		s.log.Printf("route: %q, %d points, for the G1000", route.Name, len(route.Points))
	}

	return route
}

// newestRoute is the newest route, nil without one, read again only when
// its file changed.
func (s *Server) newestRoute() *Route {
	s.mu.Lock()
	defer s.mu.Unlock()

	return s.newestLocked()
}

// newestLocked is newestRoute, with mu held.
func (s *Server) newestLocked() *Route {
	f, err := newest(s.dir)
	if err != nil {
		if s.err == nil || s.err.Error() != err.Error() {
			s.log.Printf("route: %v", err)
		}

		s.err, s.route, s.read = err, nil, file{mod: time.Time{}, path: "", size: 0}

		return nil
	}

	if !f.same(s.read) {
		s.read = f
		s.route, s.err = load(f.path)

		if s.err != nil {
			s.log.Printf("route: %s: %v", filepath.Base(f.path), s.err)
		}
	}

	return s.route
}

var errNoRoutes = errors.New("no saved SkyDemon route yet")

// maxDepth is how deep below dir routes are looked for: SkyDemon's route
// folders, and folders in them.
const maxDepth = 3

// newest is the .flightplan saved last in dir or the folders below it.
func newest(dir string) (file, error) {
	_, err := os.Stat(dir)
	if err != nil {
		return file{}, fmt.Errorf("SkyDemon's routes: %w", err)
	}

	var best file

	// Links to folders aren't followed: WalkDir doesn't. Errors below dir,
	// e.g. a folder gone since the listing, skip that part only.
	_ = filepath.WalkDir(dir, func(path string, entry fs.DirEntry, err error) error {
		if err != nil {
			return nil //nolint:nilerr // skip what can't be read
		}

		if entry.IsDir() {
			return tooDeep(dir, path)
		}

		f, ok := route(path, entry)
		if ok && (best.path == "" || f.mod.After(best.mod)) {
			best = f
		}

		return nil
	})

	if best.path == "" {
		return file{}, fmt.Errorf("%w in %s", errNoRoutes, dir)
	}

	return best, nil
}

// tooDeep is filepath.SkipDir for a folder more than maxDepth-1 below dir.
func tooDeep(dir, path string) error {
	rel, _ := filepath.Rel(dir, path)
	if rel != "." && strings.Count(rel, string(filepath.Separator)) >= maxDepth-1 {
		return filepath.SkipDir
	}

	return nil
}

// route is the file at path, if it is a route.
func route(path string, entry fs.DirEntry) (file, bool) {
	// Not IsRegular: in a Documents folder synced by OneDrive the files are
	// reparse points, which Go reports as irregular.
	if !strings.EqualFold(filepath.Ext(path), ext) {
		return file{}, false
	}

	info, err := entry.Info()
	if err != nil || info.IsDir() {
		return file{}, false // gone since the listing, or a link to a folder
	}

	return file{mod: info.ModTime(), path: path, size: info.Size()}, true
}

func load(path string) (*Route, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, fmt.Errorf("open: %w", err)
	}

	defer func() { _ = f.Close() }()

	b, err := io.ReadAll(io.LimitReader(f, maxFile+1))
	if err != nil {
		return nil, fmt.Errorf("read: %w", err)
	}

	if len(b) > maxFile {
		return nil, errTooBig
	}

	plan, err := flightplan.Parse(bytes.NewReader(b))
	if err != nil {
		return nil, fmt.Errorf("parse: %w", err)
	}

	h := fnv.New64a()
	_, _ = h.Write(b) // a hash doesn't fail

	out := &Route{
		ID:      strconv.FormatUint(h.Sum64(), 16),
		Name:    strings.TrimSuffix(filepath.Base(path), filepath.Ext(path)),
		Points:  make([]Point, 0, len(plan.Points)),
		LevelFt: plan.LevelFt,
		VFR:     plan.VFR,
	}

	for _, p := range plan.Points {
		out.Points = append(out.Points, Point{Lat: p.Lat, Lon: p.Lon, Aerodrome: p.Aerodrome})
	}

	return out, nil
}
