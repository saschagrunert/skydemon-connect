// Package gtn writes the route last saved in SkyDemon as a Garmin flight
// plan for the TDS GTNXi, which imports .gfp files from its FPL folder.
//
// SkyDemon saves positions only, so each of its aerodromes is looked up
// among the sim's airports by its position.
package gtn

import (
	"errors"
	"fmt"
	"log"
	"math"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"time"

	"github.com/saschagrunert/skydemon-connect/internal/position"
	"github.com/saschagrunert/skydemon-connect/internal/routeserver"
)

// Name is the flight plan's name in the GTN's import list. One fixed file:
// the GTN lists at most 50, and a "-" or a space in the name can hide its
// Import key.
const Name = "SKYDEMON"

const (
	fileMode = 0o644
	// matchM is how far SkyDemon's aerodrome may be from the sim's: their
	// reference points differ by a few hundred metres.
	matchM         = 1852
	earthDiameterM = 2 * 6371008.8
	minutesPerDeg  = 60
	// tenthsPerDegree: GFP positions are degrees and minutes to a tenth.
	tenthsPerDegree = minutesPerDeg * 10
	latDegWidth     = 2
	lonDegWidth     = 3
	// skyDemonDecimals is how SkyDemon rounds positions before writing
	// them, as its GFP export shows.
	skyDemonDecimals = 1e6
	// nearDeg is a latitude difference too large for matchM: a degree is
	// 111 km.
	nearDeg = 0.1
	// retryAfter is when a failed write is tried again, e.g. one the GTN
	// held the file during.
	retryAfter = 5 * time.Second
)

var errNotDir = errors.New("is not a folder")

// Folder is the TDS GTNXi's FPL folder below programData, where the
// %PROGRAMDATA% environment variable points; "" when the GTNXi isn't
// installed.
func Folder(programData string) string {
	dir := filepath.Join(programData, "TDS", "GTNXi", "FPL")
	if !filepath.IsAbs(dir) {
		return ""
	}

	fi, err := os.Stat(dir)
	if err != nil || !fi.IsDir() {
		return ""
	}

	return dir
}

// Writer writes the route as Name.gfp into a folder whenever the route or
// the airports change.
type Writer struct {
	log *log.Logger
	dir string

	mu       sync.Mutex
	route    *routeserver.Route
	airports []position.Airport
	// written is the plan last written, "" before the first.
	written string
	// retry is the pending retry of a failed write, nil without one;
	// failed is the plan whose failure was logged, to log it once.
	retry  *time.Timer
	failed string
	// closed: no more writes, nor retries.
	closed bool
}

// New returns a Writer into dir.
func New(dir string, l *log.Logger) *Writer {
	return &Writer{
		log:      l,
		dir:      dir,
		mu:       sync.Mutex{},
		route:    nil,
		airports: nil,
		written:  "",
		retry:    nil,
		failed:   "",
		closed:   false,
	}
}

// Dir is the folder written to.
func (w *Writer) Dir() string { return w.dir }

// SetRoute hands over the newest route.
func (w *Writer) SetRoute(r *routeserver.Route) {
	w.mu.Lock()
	defer w.mu.Unlock()

	w.route = r
	w.write()
}

// SetAirports hands over the sim's airports, as they come once a session.
func (w *Writer) SetAirports(a []position.Airport) {
	w.mu.Lock()
	defer w.mu.Unlock()

	w.airports = a
	w.write()
}

// Close stops writing, and a retry pending.
func (w *Writer) Close() {
	w.mu.Lock()
	defer w.mu.Unlock()

	w.closed = true

	if w.retry != nil {
		w.retry.Stop()
		w.retry = nil
	}
}

// write writes the plan if it changed. Without the airports yet, the
// aerodromes are written by their positions, and again with their codes
// once the airports came.
func (w *Writer) write() {
	if w.route == nil || w.closed {
		return
	}

	plan, idents := GFP(w.route, w.airports)
	if plan == w.written {
		return
	}

	path := filepath.Join(w.dir, Name+".gfp")

	// The plan the file holds may be this route's with more codes, e.g.
	// at the start before the airports came, or from a whole list while
	// these are a short one: kept, until the airports give as many.
	if moreCodes(path, w.route, len(idents)) {
		w.written = plan

		return
	}

	err := replaceFile(path, []byte(plan))
	if err != nil {
		w.failedWrite(plan, err)

		return
	}

	w.failed = ""

	if w.retry != nil {
		w.retry.Stop()
		w.retry = nil
	}

	w.written = plan

	via := "with the airports " + strings.Join(idents, ", ")
	if len(idents) == 0 {
		via = "by positions only"
	}

	w.log.Printf("route: %q for the GTN as %s, %s", w.route.Name, Name, via)
}

// identRE finds an airport's code before its position in a GFP point.
var identRE = regexp.MustCompile(`F:[A-Z0-9]+,`)

// moreCodes reports whether the plan at path is r's with more than n
// airport codes.
func moreCodes(path string, r *routeserver.Route, n int) bool {
	b, err := os.ReadFile(path)
	if err != nil {
		return false
	}

	positions, _ := GFP(r, nil)
	codes := identRE.FindAllIndex(b, -1)

	return len(codes) > n && string(identRE.ReplaceAll(b, []byte("F:"))) == positions
}

// failedWrite logs, once per plan, that writing it failed, and tries again
// later.
func (w *Writer) failedWrite(plan string, err error) {
	if plan != w.failed {
		w.failed = plan
		w.log.Printf("route: GTN flight plan: %v; trying again", err)
	}

	if w.retry == nil {
		w.retry = time.AfterFunc(retryAfter, w.again)
	}
}

// again is the retry of a failed write.
func (w *Writer) again() {
	w.mu.Lock()
	defer w.mu.Unlock()

	w.retry = nil
	w.write()
}

// GFP is r as a Garmin flight plan, written as SkyDemon writes its own:
// every point as F:, an aerodrome found among airports as its code and
// position, every other point as its position alone. idents are the
// airports found.
func GFP(r *routeserver.Route, airports []position.Airport) (string, []string) {
	parts := make([]string, 0, len(r.Points))

	var idents []string

	for _, p := range r.Points {
		pos := coord(p.Lat, latDegWidth, "N", "S") + coord(p.Lon, lonDegWidth, "E", "W")

		if p.Aerodrome {
			if a, ok := nearest(airports, p.Lat, p.Lon); ok {
				pos = a.Ident + "," + pos
				idents = append(idents, a.Ident)
			}
		}

		parts = append(parts, "F:"+pos)
	}

	return "FPN/RI:" + strings.Join(parts, ":"), idents
}

// nearest is the airport nearest to lat, lon within matchM.
func nearest(airports []position.Airport, lat, lon float64) (position.Airport, bool) {
	best, bestM := position.Airport{Ident: "", Lat: 0, Lon: 0}, math.Inf(1)

	for _, a := range airports {
		// Far ones go without the trigonometry.
		if math.Abs(a.Lat-lat) > nearDeg {
			continue
		}

		if m := distanceM(lat, lon, a.Lat, a.Lon); m < bestM {
			best, bestM = a, m
		}
	}

	return best, bestM <= matchM
}

func distanceM(lat1, lon1, lat2, lon2 float64) float64 {
	const rad = math.Pi / 180

	const halfRad = rad / 2

	sinLat, sinLon := math.Sin((lat2-lat1)*halfRad), math.Sin((lon2-lon1)*halfRad)
	a := sinLat*sinLat + math.Cos(lat1*rad)*math.Cos(lat2*rad)*sinLon*sinLon

	return earthDiameterM * math.Asin(math.Min(1, math.Sqrt(a)))
}

// coord writes an angle as GFP does, degrees and minutes to a tenth
// without separators: 51.423985 is N51254, 12.236389 E012142.
func coord(v float64, degWidth int, pos, neg string) string {
	hemi := pos
	if v < 0 {
		hemi, v = neg, -v
	}

	// Midpoints round to even, as SkyDemon (.NET's Math.Round) does, of
	// the position as SkyDemon keeps it.
	v = math.RoundToEven(v*skyDemonDecimals) / skyDemonDecimals
	tenths := int(math.RoundToEven(v * tenthsPerDegree))
	deg, rest := tenths/tenthsPerDegree, tenths%tenthsPerDegree

	return fmt.Sprintf("%s%0*d%03d", hemi, degWidth, deg, rest)
}

// replaceFile writes data to path through a new file in the same folder,
// renamed over path. The FPL folder is under %PROGRAMDATA%, where other
// users of the PC can create files: a link planted under path's name is
// replaced, not followed, and the GTN never reads a half-written plan.
func replaceFile(path string, data []byte) error {
	dir := filepath.Dir(path)

	fi, err := os.Stat(dir)
	if err != nil {
		return fmt.Errorf("GTN folder: %w", err)
	}

	if !fi.IsDir() {
		return fmt.Errorf("GTN folder %s %w", dir, errNotDir)
	}

	f, err := os.CreateTemp(dir, ".skydemonconnect-*.tmp")
	if err != nil {
		return fmt.Errorf("create: %w", err)
	}

	tmp := f.Name()
	_, writeErr := f.Write(data)
	closeErr := f.Close()

	err = errors.Join(writeErr, closeErr)
	if err == nil {
		err = os.Chmod(tmp, fileMode)
	}

	if err == nil {
		err = os.Rename(tmp, path)
	}

	if err != nil {
		_ = os.Remove(tmp)

		return fmt.Errorf("write %s: %w", filepath.Base(path), err)
	}

	return nil
}
