// Package feed sends the newest aircraft position to the outputs at a fixed
// rate and reports its status to the UI.
package feed

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log"
	"net/netip"
	"slices"
	"sync"
	"time"

	"github.com/saschagrunert/skydemon-connect/internal/foreflight"
	"github.com/saschagrunert/skydemon-connect/internal/nmea"
	"github.com/saschagrunert/skydemon-connect/internal/position"
	"github.com/saschagrunert/skydemon-connect/internal/report"
)

// RateHz is the update rate. Real GPS receivers send 1-10 fixes a second; 30
// matches the sim's frame rate closely enough for a smooth moving map.
const RateHz = 30

// staleAfter is how long a position may stay where it is before it is sent
// as standing still. A paused or hung sim sends no frames, or the same
// position again; EFBs that extrapolate between fixes would otherwise move
// a frozen aircraft on at its last ground speed.
const staleAfter = 2 * time.Second

// outputSettle is how long an output must keep failing before it counts as
// failing, logged and shown, and keep working before it counts as working
// again: one that flaps, e.g. a send failing every other time, doesn't log
// and pop a balloon on every flip.
const outputSettle = 2 * time.Second

// What the tray says about failing UDP output; the details go to the log.
// Each fits the tray tooltip.
const (
	udpFailsText  = "UDP: sending to every -udp target fails, see the log"
	noTargetsText = "UDP: no -udp host name resolves, see the log"
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
	// Connected: a session with MSFS is up, in a flight or not.
	Connected bool
}

// TooltipTitle is the tray tooltip's first line; Status.String follows.
const TooltipTitle = "SkyDemonConnect\n"

// MaxErrLen is the room for Status.Err in the tray's tooltip: 127
// characters, of which TooltipTitle and the "Error: " that String puts
// first take 23. Texts meant for the user fit it.
const MaxErrLen = 127 - len(TooltipTitle) - len(errorPrefix)

// errorPrefix starts the String of a Failed Status.
const errorPrefix = "Error: "

// String is a one-line description, used for the console and the tooltip.
func (s Status) String() string {
	switch s.Kind {
	case Failed:
		return errorPrefix + s.Err
	case Sending:
		return fmt.Sprintf("%d Hz · %.0f ft · %.0f kt · trk %03.0f",
			RateHz, s.Sample.AltM/position.FeetToMeters, s.Sample.GSKnots,
			position.RoundAngle(s.Sample.TrackTrue, 0))
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

type fix struct {
	at     time.Time
	sample position.Sample
}

// Feed holds the shared state between the simulator input, the senders
// and the UI. All methods are safe for concurrent use.
//
// A position is sent while the sim is live (connected and in a flight) and
// reported one since it became live. A paused sim sends no new frames, but
// a real GPS keeps reporting, so the last position is repeated, standing
// still, until the sim is no longer live.
type Feed struct {
	nmea chan fix
	errs *report.Reporter
	out  Outputs
	// moved is when sample last moved: a new frame at the same position
	// doesn't make it fresh.
	moved   time.Time
	targets []netip.AddrPort
	// err is the input's error, from SetError. udpErr and serialErr are
	// set while every UDP send, or the serial output, fails.
	err, udpErr, serialErr string
	sample                 position.Sample
	mu                     sync.Mutex
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

	if !f.have || s.Lat != f.sample.Lat || s.Lon != f.sample.Lon {
		f.moved = time.Now()
	}

	f.sample, f.have, f.err = s, true, ""
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
		f.udpErr, f.serialErr = "", ""
		f.errs.Forget("feed: udp")
		f.errs.Forget("feed: serial")

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

	st := f.stateLocked(time.Now())

	return Status{Sample: st.sample, Err: st.why, Kind: st.kind, Connected: f.connected}
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

	tick := time.NewTicker(time.Second / RateHz)
	defer tick.Stop()

	for {
		select {
		case <-ctx.Done():
			return
		case now := <-tick.C:
			f.mu.Lock()
			st := f.stateLocked(now)
			f.mu.Unlock()

			if st.send {
				f.send(st.sample, now)
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
// to send at now, whether to send it and the Kind the UI shows. Positions
// are also sent while an output fails, so that it is retried.
func (f *Feed) stateLocked(now time.Time) state {
	st := state{sample: f.sample, kind: Sending, send: f.err == "" && f.have && f.live}
	if now.Sub(f.moved) > staleAfter {
		st.sample.GSKnots = 0
	}

	switch {
	case f.err != "":
		st.kind, st.why = Failed, f.err
	case !st.send:
		st.kind = Waiting
	case f.udpErr != "":
		st.kind, st.why = Failed, f.udpErr
	case f.serialErr != "":
		st.kind, st.why = Failed, f.serialErr
	}

	return st
}

func (f *Feed) send(s position.Sample, now time.Time) {
	if f.out.UDP != nil {
		f.sendUDP(s)
	}

	if f.out.Serial != nil {
		f.queueNMEA(fix{sample: s, at: now})
	}
}

// udpKey is the Reporter key of the sends to t.
func udpKey(t netip.AddrPort) string { return "feed: udp " + t.String() }

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

	f.udpDone(dests)
}

// udpDone sets what the UI says about UDP after a send to dests: it fails
// while every target counts as failing, or while there is none and no
// serial output either. Each target's errors are logged by sendUDP.
func (f *Feed) udpDone(dests []netip.AddrPort) {
	if len(dests) == 0 && f.out.Serial == nil {
		f.errs.Report("feed: udp", errNoTargets)
		f.setOutputErr(&f.udpErr, failingText(f.errs.Failing("feed: udp"), noTargetsText))

		return
	}

	f.errs.Report("feed: udp", nil)

	allFailing := len(dests) > 0 && !slices.ContainsFunc(dests, func(t netip.AddrPort) bool {
		return !f.errs.Failing(udpKey(t))
	})
	f.setOutputErr(&f.udpErr, failingText(allFailing, udpFailsText))
}

// failingText is msg while failing, "" otherwise.
func failingText(failing bool, msg string) string {
	if failing {
		return msg
	}

	return ""
}

// setOutputErr records what the UI says about a failing output, "" when it
// works.
func (f *Feed) setOutputErr(field *string, msg string) {
	f.mu.Lock()
	*field = msg
	f.mu.Unlock()
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
			if errors.Is(err, io.ErrShortWrite) && n > 0 {
				err = nil // a slow port, or one nobody reads, still works
			}

			f.errs.Report("feed: serial", err)
			f.setOutputErr(&f.serialErr,
				failingText(f.errs.Failing("feed: serial"), serialFails(f.out.SerialName)))
		}
	}
}
