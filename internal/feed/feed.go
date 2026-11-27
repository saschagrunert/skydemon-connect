// Package feed sends the newest aircraft position to the outputs at a fixed
// rate and reports its status to the UI.
package feed

import (
	"cmp"
	"context"
	"errors"
	"fmt"
	"io"
	"log"
	"math"
	"net/netip"
	"slices"
	"sync"
	"time"

	"github.com/saschagrunert/skydemon-connect/internal/foreflight"
	"github.com/saschagrunert/skydemon-connect/internal/nmea"
	"github.com/saschagrunert/skydemon-connect/internal/position"
	"github.com/saschagrunert/skydemon-connect/internal/report"
	"github.com/saschagrunert/skydemon-connect/internal/serial"
)

// RateHz is the update rate. Real GPS receivers send 1-10 fixes a second; 30
// matches the sim's frame rate closely enough for a smooth moving map.
const RateHz = 30

// A position that doesn't change is sent as standing still: EFBs that
// extrapolate between fixes would otherwise move a frozen aircraft on at
// its last ground speed. A paused sim sends the same position again, which
// counts after staleFrames frames (half a second at 30 fps); a hung one
// sends nothing, which counts after staleAfter. The count, not a time, is
// used for frames, so that a sim that runs slowly isn't taken for paused.
const (
	staleFrames = 15
	staleAfter  = 2 * time.Second
)

// maxAhead caps how far a position is moved on to the time it is sent: a
// fix is sent at RateHz, whatever the sim's frame rate, so it is moved on
// along its track by the time since its frame came, or since it last moved. Without that, at 45
// fps the step between packets alternates between one and two frames, at
// 25 fps every sixth packet repeats one. Past maxAhead the sim is late,
// e.g. stuttering, and guessing further would only overshoot.
const maxAhead = 100 * time.Millisecond

// outputSettle is how long an output must keep failing before it counts as
// failing, logged and shown, and keep working before it counts as working
// again: one that flaps, e.g. a send failing every other time, doesn't log
// and pop a balloon on every flip.
const outputSettle = 2 * time.Second

// The Reporter keys of the outputs; each UDP target has its own, udpKey.
const (
	keyUDP    = "feed: udp"
	keySerial = "feed: serial"
)

// What the tray says about failing UDP output; the details go to the log.
// Each fits MaxErrLen.
const (
	udpFailsText  = "UDP: sending to every target fails, see the log"
	noTargetsText = "UDP: no target host name resolves, see the log"
)

// errNoTargets is logged while no UDP target resolves.
var errNoTargets = errors.New("no target resolves")

// serialFails is what the tray says about a failing COM port, named name.
func serialFails(name string) string {
	if name == "" {
		name = "COM port"
	}

	return name + ": not working (is it plugged in, and an app reading the other end?), see the log"
}

// Kind is the coarse state the tray icon shows.
type Kind int

const (
	// Waiting means there is no position to send: the sim is not running,
	// loading or in its menu. Status.Connected tells which.
	Waiting Kind = iota
	// Sending means positions are being sent.
	Sending
	// Failed means a problem that needs the user, e.g. no SimConnect.dll,
	// or an output that every send fails on.
	Failed
)

// Status is a snapshot for the UI.
type Status struct {
	Sample position.Sample
	Err    string
	Kind   Kind
	// FrameRate is how many frames a second the sim sent over the last
	// second, 0 when not known: positions go out at RateHz regardless, so
	// this shows how often they are new.
	FrameRate int
	// Connected: a session with MSFS is up, in a flight or not.
	Connected bool
}

// ErrorPrefix starts the String of a Failed Status.
const ErrorPrefix = "Error: "

// MaxErrLen is the room for Status.Err in the tray's tooltip: its 127
// characters, less the title line "SkyDemonConnect\n" and ErrorPrefix.
// The failures the feed reports fit it.
const MaxErrLen = 127 - len("SkyDemonConnect\n") - len(ErrorPrefix)

// String is a one-line description, used for the console and the tooltip.
func (s Status) String() string {
	switch s.Kind {
	case Failed:
		return ErrorPrefix + s.Err
	case Sending:
		pos := fmt.Sprintf("%.0f ft · %.0f kt · trk %03.0f",
			s.Sample.AltM/position.FeetToMeters, s.Sample.GSKnots,
			position.RoundAngle(s.Sample.TrackTrue, 0))
		if s.FrameRate > 0 {
			return fmt.Sprintf("%d fps · %s", s.FrameRate, pos)
		}

		return pos
	case Waiting:
	}

	if s.Connected {
		return "Connected to MSFS, waiting for a flight"
	}

	return "Waiting for MSFS…"
}

// UDPWriter sends a datagram; *net.UDPConn is one.
type UDPWriter interface {
	WriteToUDPAddrPort(b []byte, addr netip.AddrPort) (int, error)
}

// Outputs are the destinations of the feed. At least one must be set. They
// don't change after New; the UDP destinations are set with SetTargets.
type Outputs struct {
	// UDP sends ForeFlight XGPS (and XATT) messages to the targets. Leave
	// it nil (not a nil *net.UDPConn) when there is no UDP output.
	UDP UDPWriter
	// Name is the simulator name in the messages.
	Name       string
	NoAttitude bool
	// Serial receives NMEA sentences, e.g. a (virtual) COM port, called
	// SerialName in messages.
	Serial     io.Writer
	SerialName string
}

// fix is a position to write as NMEA, and when it was taken.
type fix struct {
	at     time.Time
	sample position.Sample
}

// Feed holds the shared state between the simulator input, the senders
// and the UI. All methods are safe for concurrent use.
//
// A position is sent while the sim is live (connected and in a flight) and
// has sent one since it became live. A paused sim sends no new position,
// but a real GPS keeps reporting, so the last one is repeated, standing
// still, until the sim is no longer live.
type Feed struct {
	nmea chan fix
	errs *report.Reporter
	out  Outputs
	// moved is when sample came, or last moved: a new frame at the same
	// position doesn't make it fresh. same counts the frames since that
	// did not move.
	moved   time.Time
	same    int
	frames  frameRate
	targets []netip.AddrPort
	// traffic are the other aircraft, sent at TrafficHz; trafficFunc,
	// when set, makes them instead.
	traffic     []position.Traffic
	trafficFunc TrafficFunc
	// err is the input's error, from SetError. Whether an output fails
	// is errs's to say.
	err    string
	sample position.Sample
	mu     sync.Mutex
	// have is true while sample is a position from the current session.
	have bool
	// live is true while the sim is connected and in a flight.
	live bool
	// connected is true while a session with the sim is up.
	connected bool
}

// New returns a feed sending to out, logging output failures to l.
func New(out Outputs, l *log.Logger) *Feed {
	return &Feed{nmea: make(chan fix, 1), out: out, errs: report.NewSettled(l, outputSettle)}
}

// Update stores the newest sample from the sim. A sample at 0°/0° means
// that the sim is loading or in its menu: the feed stops sending until a
// real position arrives, so that the EFB doesn't show the last flight.
// Samples with NaN or infinite values are dropped.
func (f *Feed) Update(s position.Sample) {
	if !s.IsFinite() {
		return
	}

	f.mu.Lock()
	defer f.mu.Unlock()

	if s.IsNullIsland() {
		f.have = false

		return
	}

	now := time.Now()
	f.frames.count(now)

	if !f.have || s.Lat != f.sample.Lat || s.Lon != f.sample.Lon {
		f.moved, f.same = now, 0
	} else {
		f.same++
	}

	f.sample, f.have, f.err = s, true, ""
}

// frameRate measures how many frames a second the sim sends, over windows
// of a second.
type frameRate struct {
	start, last time.Time
	frames      int
	perSecond   int
}

func (r *frameRate) count(now time.Time) {
	// After a gap, e.g. a pause, measure afresh: the frames from before it
	// over the whole gap would give a rate far too low.
	if r.start.IsZero() || now.Sub(r.last) > time.Second {
		r.start, r.frames, r.perSecond = now, 0, 0
	}

	if d := now.Sub(r.start); d >= time.Second {
		r.perSecond = int(math.Round(float64(r.frames) / d.Seconds()))
		r.start, r.frames = now, 0
	}

	r.frames++
	r.last = now
}

// at returns the rate at now: 0 once no frame came for a second.
func (r *frameRate) at(now time.Time) int {
	if now.Sub(r.last) > time.Second {
		return 0
	}

	return r.perSecond
}

// SetLive records whether the sim is connected and in a flight. Either
// change starts over: a new session doesn't repeat the previous one's last
// position, and an ended one stops the feed, so that the EFB shows the GPS
// as lost.
func (f *Feed) SetLive(live bool) {
	f.mu.Lock()
	defer f.mu.Unlock()

	if live != f.live {
		f.live, f.have = live, false

		// Outputs are judged afresh: an error from before, e.g. of a COM
		// port that came back meanwhile, would otherwise show as a new
		// problem until the next writes settle.
		f.errs.Forget(keyUDP)
		f.errs.Forget(keySerial)

		for _, t := range f.targets {
			f.errs.Forget(udpKey(t))
		}
	}
}

// SetConnected records whether a session with the sim is up, in a flight
// or not, for Status.
func (f *Feed) SetConnected(connected bool) {
	f.mu.Lock()
	defer f.mu.Unlock()

	f.connected = connected
}

// SetError puts the feed into the Failed state until the next Update or
// SetError(nil).
func (f *Feed) SetError(err error) {
	f.mu.Lock()
	defer f.mu.Unlock()

	f.err = ""
	if err != nil {
		f.err = err.Error()
	}
}

// Targets returns the UDP destinations.
func (f *Feed) Targets() []netip.AddrPort {
	f.mu.Lock()
	defer f.mu.Unlock()

	return slices.Clone(f.targets)
}

// SetTargets replaces the UDP destinations, e.g. after a network change.
func (f *Feed) SetTargets(t []netip.AddrPort) {
	f.mu.Lock()
	defer f.mu.Unlock()

	// A target that comes back later and fails the same way is news.
	for _, old := range f.targets {
		if !slices.Contains(t, old) {
			f.errs.Forget(udpKey(old))
		}
	}

	f.targets = t
}

// Status returns a snapshot for the UI.
func (f *Feed) Status() Status {
	f.mu.Lock()
	defer f.mu.Unlock()

	now := time.Now()
	st := f.stateLocked(now)

	return Status{
		Sample:    st.sample,
		Err:       st.why,
		Kind:      st.kind,
		FrameRate: f.frames.at(now),
		Connected: f.connected,
	}
}

// TrafficHz is how often the other aircraft are sent. Traffic receivers
// report each target about once a second.
const TrafficHz = 1

// MaxTraffic is how many of the other aircraft are sent, see nearest: a
// busy airport with injected traffic can have hundreds.
const MaxTraffic = 50

// parkedKnots is the ground speed below which a target on the ground
// counts as parked.
const parkedKnots = 1.0

// How much a target matters, see nearest.
const (
	rankAirborne = iota
	rankMoving
	rankParked
)

// SetTraffic replaces the other aircraft sent while positions are.
func (f *Feed) SetTraffic(t []position.Traffic) {
	f.mu.Lock()
	f.traffic = t
	f.mu.Unlock()
}

// TrafficFunc makes the other aircraft around own, the position about to
// be sent at now.
type TrafficFunc func(own position.Sample, now time.Time) []position.Traffic

// SetTrafficFunc makes the traffic sent with fn, from Run's goroutine,
// instead of SetTraffic's.
func (f *Feed) SetTrafficFunc(fn TrafficFunc) {
	f.mu.Lock()
	f.trafficFunc = fn
	f.mu.Unlock()
}

// Run sends until ctx is done, and returns once nothing writes to the
// outputs anymore, so they can be closed.
func (f *Feed) Run(ctx context.Context) {
	var wg sync.WaitGroup
	defer wg.Wait()

	if f.out.Serial != nil {
		// A slow or unread COM port must not delay UDP, so NMEA is
		// written apart from the ticker.
		wg.Go(func() { f.runSerial(ctx) })
	}

	if f.out.UDP != nil {
		// Hundreds of datagrams a second, should writes block, must not
		// delay the positions either.
		wg.Go(func() { f.runTraffic(ctx) })
	}

	tick := time.NewTicker(time.Second / RateHz)
	defer tick.Stop()

	for {
		select {
		case <-ctx.Done():
			return
		case now := <-tick.C:
			f.mu.Lock()
			s, send := f.toSendLocked(now)
			f.mu.Unlock()

			if send {
				f.send(s, now)
			}
		}
	}
}

// state is what the feed does at one moment.
type state struct {
	why    string // for Failed
	sample position.Sample
	kind   Kind
	send   bool
}

// stateLocked is the one place that decides what the feed does: the sample
// at now, whether to send it and the Kind the UI shows. Positions are also
// sent while an output fails, so that it is retried.
func (f *Feed) stateLocked(now time.Time) state {
	st := state{sample: f.sample, kind: Sending, send: f.err == "" && f.have && f.live}
	if f.same >= staleFrames || now.Sub(f.moved) > staleAfter {
		st.sample.GSKnots = 0
	}

	switch {
	case f.err != "":
		st.kind, st.why = Failed, f.err
	case !st.send:
		st.kind = Waiting
	default:
		st.why = f.outputFailureLocked()
		if st.why != "" {
			st.kind = Failed
		}
	}

	return st
}

// toSendLocked returns the position to send at now, moved on by up to
// maxAhead (see there) from when it last moved, and whether to send one.
// Once it counts as standing still it is sent with ground speed 0, but
// still moved on with its own: it stays where the last packets put it
// instead of stepping back.
func (f *Feed) toSendLocked(now time.Time) (position.Sample, bool) {
	st := f.stateLocked(now)

	s := f.sample.Ahead(min(now.Sub(f.moved), maxAhead))
	s.GSKnots = st.sample.GSKnots

	return s, st.send
}

// outputFailureLocked is what the UI says about a failing output, "" while
// they work: UDP fails while every target counts as failing, or while
// there is none and no serial output either.
func (f *Feed) outputFailureLocked() string {
	if f.out.UDP != nil {
		switch {
		case len(f.targets) == 0:
			// keyUDP fails only without serial output, see sendUDP.
			if f.errs.Failing(keyUDP) {
				return noTargetsText
			}
		case !slices.ContainsFunc(f.targets, func(t netip.AddrPort) bool {
			return !f.errs.Failing(udpKey(t))
		}):
			return udpFailsText
		}
	}

	if f.out.Serial != nil && f.errs.Failing(keySerial) {
		return serialFails(f.out.SerialName)
	}

	return ""
}

// send sends s to every output. NMEA goes out at the full rate too, not
// the 1-10 Hz of GPS receivers: a smooth map is the point, and a slow port
// limits itself, since only the newest fix waits for it.
func (f *Feed) send(s position.Sample, now time.Time) {
	if f.out.UDP != nil {
		f.sendUDP(s)
	}

	if f.out.Serial != nil {
		f.queueNMEA(fix{sample: s, at: now})
	}
}

// udpKey is the Reporter key of the sends to t.
func udpKey(t netip.AddrPort) string { return keyUDP + " " + t.String() }

// sendUDP sends s to every target. UDP fails while every target does, or
// while there is none and no serial output either.
func (f *Feed) sendUDP(s position.Sample) {
	gps := []byte(foreflight.XGPS(f.out.Name, s))

	var att []byte
	if !f.out.NoAttitude {
		att = []byte(foreflight.XATT(f.out.Name, s))
	}

	f.mu.Lock()
	dests := f.targets
	f.mu.Unlock()

	for _, t := range dests {
		_, err := f.out.UDP.WriteToUDPAddrPort(gps, t)
		if err == nil && att != nil {
			_, err = f.out.UDP.WriteToUDPAddrPort(att, t)
		}

		f.errs.Report(udpKey(t), err)
	}

	// Having no target is a failure only without serial output.
	var none error
	if len(dests) == 0 && f.out.Serial == nil {
		none = errNoTargets
	}

	f.errs.Report(keyUDP, none)
}

// runTraffic sends the traffic at TrafficHz while positions are sent,
// until ctx is done.
func (f *Feed) runTraffic(ctx context.Context) {
	tick := time.NewTicker(time.Second / TrafficHz)
	defer tick.Stop()

	for {
		select {
		case <-ctx.Done():
			return
		case now := <-tick.C:
			f.mu.Lock()
			own, send := f.toSendLocked(now)
			traffic, fn, dests := f.traffic, f.trafficFunc, f.targets
			f.mu.Unlock()

			if !send {
				continue
			}

			if fn != nil {
				traffic = fn(own, now)
			}

			f.sendTraffic(nearest(own, traffic), dests)
		}
	}
}

// nearest returns the MaxTraffic targets in traffic that matter most to
// own, or all of them if they are no more: the airborne ones, then those moving on
// the ground, then the parked ones, each nearest first. Hundreds parked at
// a big airport would otherwise push out the aircraft on final, and a
// real receiver doesn't see most of them, their transponders off.
func nearest(own position.Sample, traffic []position.Traffic) []position.Traffic {
	if len(traffic) <= MaxTraffic {
		return traffic
	}

	rank := func(t position.Traffic) int {
		switch {
		case t.Airborne:
			return rankAirborne
		case t.GSKnots >= parkedKnots:
			return rankMoving
		default:
			return rankParked
		}
	}

	// Flat, and squared: only the order counts.
	k := math.Cos(own.Lat / position.DegPerRad)
	dist := func(t position.Traffic) float64 {
		dLat, dLon := t.Lat-own.Lat, position.Norm180(t.Lon-own.Lon)*k

		return dLat*dLat + dLon*dLon
	}

	sorted := slices.Clone(traffic)
	slices.SortFunc(sorted, func(a, b position.Traffic) int {
		return cmp.Or(cmp.Compare(rank(a), rank(b)), cmp.Compare(dist(a), dist(b)))
	})

	return sorted[:MaxTraffic]
}

// sendTraffic sends every target in traffic to dests. Failures show in the
// position sends, which go to the same destinations.
func (f *Feed) sendTraffic(traffic []position.Traffic, dests []netip.AddrPort) {
	for _, tr := range traffic {
		msg := []byte(foreflight.XTRAFFIC(f.out.Name, tr))

		for _, t := range dests {
			_, _ = f.out.UDP.WriteToUDPAddrPort(msg, t)
		}
	}
}

// queueNMEA hands x to the serial writer. While the writer is still busy,
// x replaces the fix waiting for it, so that the next write is the newest
// position rather than one that was queued a write earlier.
func (f *Feed) queueNMEA(x fix) {
	select {
	case <-f.nmea:
	default:
	}

	// send is the only sender, so there is room now.
	f.nmea <- x
}

func (f *Feed) runSerial(ctx context.Context) {
	for {
		select {
		case <-ctx.Done():
			return
		case x := <-f.nmea:
			n, err := io.WriteString(f.out.Serial, nmea.Sentences(x.sample, x.at))
			if serial.Working(n, err) {
				err = nil
			}

			f.errs.Report(keySerial, err)
		}
	}
}
