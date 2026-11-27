package feed

import (
	"context"
	"errors"
	"io"
	"log"
	"math"
	"net/netip"
	"slices"
	"strconv"
	"strings"
	"sync"
	"testing"
	"testing/synctest"
	"time"

	"github.com/saschagrunert/skydemon-connect/internal/position"
	"github.com/saschagrunert/skydemon-connect/internal/testutil"
)

var errBoom = errors.New("boom")

// target returns the test destination 192.0.2.n:49002.
func target(n byte) netip.AddrPort {
	return netip.AddrPortFrom(netip.AddrFrom4([4]byte{192, 0, 2, n}), 49002)
}

// moved returns s a little further north.
func moved(s position.Sample) position.Sample {
	s.Lat += 0.0001

	return s
}

// flying returns a position in flight.
func flying() position.Sample { return position.Sample{Lat: 51, Lon: 12, GSKnots: 100} }

type packet struct {
	msg string
	to  netip.AddrPort
}

// fakeUDP records datagrams and fails those to the addresses in fail.
type fakeUDP struct {
	fail map[netip.AddrPort]bool
	got  []packet
	mu   sync.Mutex
}

func (u *fakeUDP) WriteToUDPAddrPort(b []byte, a netip.AddrPort) (int, error) {
	u.mu.Lock()
	defer u.mu.Unlock()

	if u.fail[a] {
		return 0, errBoom
	}

	u.got = append(u.got, packet{to: a, msg: string(b)})

	return len(b), nil
}

func (u *fakeUDP) setFail(fail ...netip.AddrPort) {
	u.mu.Lock()
	defer u.mu.Unlock()

	u.fail = map[netip.AddrPort]bool{}
	for _, a := range fail {
		u.fail[a] = true
	}
}

// kinds returns the message types sent to dest, in order.
func (u *fakeUDP) kinds(dest netip.AddrPort) []string {
	u.mu.Lock()
	defer u.mu.Unlock()

	var kinds []string

	for _, p := range u.got {
		if p.to == dest {
			kinds = append(kinds, p.msg[:4])
		}
	}

	return kinds
}

// serialWriter records the NMEA written to it and fails while err is set.
// With block set, each write waits until release is closed.
type serialWriter struct {
	err     error
	release chan struct{}
	writes  []string
	mu      sync.Mutex
	block   bool
}

func (w *serialWriter) Write(p []byte) (int, error) {
	if w.block {
		<-w.release
	}

	w.mu.Lock()
	defer w.mu.Unlock()

	if w.err != nil {
		return 0, w.err
	}

	w.writes = append(w.writes, string(p))

	return len(p), nil
}

func (w *serialWriter) setErr(err error) {
	w.mu.Lock()
	w.err = err
	w.mu.Unlock()
}

func (w *serialWriter) written() []string {
	w.mu.Lock()
	defer w.mu.Unlock()

	return slices.Clone(w.writes)
}

func newFeed(out Outputs) (*Feed, *testutil.Buffer) {
	var logs testutil.Buffer

	return New(out, log.New(&logs, "", 0)), &logs
}

// sendSettled sends s, and again once outputSettle has passed, so that a
// failure or a recovery counts. It runs in a synctest bubble.
func sendSettled(f *Feed, s position.Sample) {
	f.send(s, time.Now())
	time.Sleep(outputSettle)
	f.send(s, time.Now())
}

func TestSendUDP(t *testing.T) {
	t.Parallel()

	synctest.Test(t, func(t *testing.T) {
		u := &fakeUDP{}
		u.setFail(target(2))

		f, logs := newFeed(Outputs{UDP: u, Name: "T"})
		f.SetTargets([]netip.AddrPort{target(1), target(2), target(3)})
		sendSettled(f, flying())
		f.send(flying(), time.Now())

		want := []string{"XGPS", "XATT", "XGPS", "XATT", "XGPS", "XATT"}
		for _, to := range []netip.AddrPort{target(1), target(3)} {
			if got := u.kinds(to); !slices.Equal(got, want) {
				t.Errorf("to %v: got %v, want %v", to, got, want)
			}
		}

		if got := u.kinds(target(2)); len(got) != 0 {
			t.Errorf("failing target: %v", got)
		}

		if n := strings.Count(logs.String(), "feed: udp 192.0.2.2:49002: boom"); n != 1 {
			t.Errorf("failure logged %d times, want 1: %q", n, logs)
		}
	})

	u := &fakeUDP{}
	f, _ := newFeed(Outputs{UDP: u, Name: "T", NoAttitude: true})
	f.SetTargets([]netip.AddrPort{target(1)})
	f.send(flying(), time.Now())

	if got := u.kinds(target(1)); !slices.Equal(got, []string{"XGPS"}) {
		t.Errorf("NoAttitude: got %v", got)
	}
}

// TestUDPFailures checks when failing UDP sends make the feed Failed: only
// when no target gets the position.
func TestUDPFailures(t *testing.T) {
	t.Parallel()

	for _, tc := range []struct {
		name    string
		targets []netip.AddrPort
		fail    []netip.AddrPort
		serial  bool
		want    Kind
		err     string
	}{
		{"all work", []netip.AddrPort{target(1), target(2)}, nil, false, Sending, ""},
		{"one of two fails", []netip.AddrPort{target(1), target(2)}, []netip.AddrPort{target(2)}, false, Sending, ""},
		{
			"all fail",
			[]netip.AddrPort{target(1), target(2)},
			[]netip.AddrPort{target(1), target(2)},
			false,
			Failed, udpFailsText,
		},
		{"no target", nil, nil, false, Failed, noTargetsText},
		{"no target, but serial", nil, nil, true, Sending, ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			u := &fakeUDP{}
			u.setFail(tc.fail...)

			out := Outputs{UDP: u, Name: "T"}
			if tc.serial {
				out.Serial = &serialWriter{}
			}

			synctest.Test(t, func(t *testing.T) {
				f, _ := newFeed(out)
				f.SetTargets(tc.targets)
				f.SetLive(true)
				f.Update(flying())
				sendSettled(f, flying())

				if st := f.Status(); st.Kind != tc.want || st.Err != tc.err {
					t.Errorf("status %v %q, want %v %q", st.Kind, st.Err, tc.want, tc.err)
				}
			})
		})
	}
}

// Each outage is logged: one with no target left, and one of a target that
// was replaced and came back.
func TestUDPOutagesLoggedEachTime(t *testing.T) {
	t.Parallel()

	synctest.Test(t, func(t *testing.T) { testUDPOutagesLoggedEachTime(t) })
}

func testUDPOutagesLoggedEachTime(t *testing.T) {
	t.Helper()

	u := &fakeUDP{}
	f, logs := newFeed(Outputs{UDP: u, Name: "T"})
	f.SetLive(true)
	f.Update(flying())

	for _, step := range []struct {
		targets []netip.AddrPort
		fail    bool
	}{
		{nil, false},                         // the tablet is gone
		{[]netip.AddrPort{target(1)}, false}, // back
		{nil, false},                         // gone again
		{[]netip.AddrPort{target(1)}, true},  // back, but failing
		{[]netip.AddrPort{target(2)}, false}, // replaced
		{[]netip.AddrPort{target(1)}, true},  // back, failing the same way
	} {
		if step.fail {
			u.setFail(step.targets...)
		}

		f.SetTargets(step.targets)
		sendSettled(f, flying())
	}

	out := logs.String()
	if n := strings.Count(out, "feed: udp: no target resolves"); n != 2 {
		t.Errorf("%d no-target messages, want 2:\n%s", n, out)
	}

	if n := strings.Count(out, "feed: udp "+target(1).String()+": boom"); n != 2 {
		t.Errorf("%d failures of the returning target, want 2:\n%s", n, out)
	}
}

func TestUDPRecovers(t *testing.T) {
	t.Parallel()

	synctest.Test(t, func(t *testing.T) {
		u := &fakeUDP{}
		u.setFail(target(1))

		f, logs := newFeed(Outputs{UDP: u, Name: "T"})
		f.SetTargets([]netip.AddrPort{target(1)})
		f.SetLive(true)
		f.Update(flying())
		sendSettled(f, flying())

		if f.Status().Kind != Failed {
			t.Fatal("the only target fails: not Failed")
		}

		u.setFail()
		sendSettled(f, flying())

		if st := f.Status(); st.Kind != Sending {
			t.Errorf("after recovering: %+v", st)
		}

		if !strings.Contains(logs.String(), "feed: udp 192.0.2.1:49002: working again") {
			t.Errorf("log %q", logs)
		}
	})
}

// A target that fails every other send, faster than outputSettle, neither
// logs on every flip nor makes the feed Failed.
func TestUDPFlappingSettles(t *testing.T) {
	t.Parallel()

	synctest.Test(t, func(t *testing.T) {
		u := &fakeUDP{}
		f, logs := newFeed(Outputs{UDP: u, Name: "T"})
		f.SetTargets([]netip.AddrPort{target(1)})
		f.SetLive(true)
		f.Update(flying())

		for i := range 3 * RateHz {
			if i%2 == 0 {
				u.setFail(target(1))
			} else {
				u.setFail()
			}

			f.send(flying(), time.Now())

			if f.Status().Kind == Failed {
				t.Fatal("a flapping target made the feed Failed")
			}

			time.Sleep(time.Second / RateHz)
		}

		if logs.String() != "" {
			t.Errorf("log %q", logs)
		}
	})
}

func TestSerialFailure(t *testing.T) {
	t.Parallel()

	synctest.Test(t, func(t *testing.T) {
		w := &serialWriter{err: errBoom}
		f, logs := newFeed(Outputs{Serial: w})
		f.SetLive(true)
		f.Update(flying())

		ctx, cancel := context.WithCancel(t.Context())
		defer cancel()

		go f.runSerial(ctx)

		f.send(flying(), time.Now())
		synctest.Wait()
		time.Sleep(outputSettle)
		f.send(flying(), time.Now())
		synctest.Wait()

		if st := f.Status(); st.Kind != Failed || st.Err != serialFails("") {
			t.Errorf("failing serial: %+v", st)
		}

		w.setErr(nil)
		f.send(flying(), time.Now())
		synctest.Wait()
		time.Sleep(outputSettle)
		f.send(flying(), time.Now())
		synctest.Wait()

		if st := f.Status(); st.Kind != Sending {
			t.Errorf("after recovering: %+v", st)
		}

		if got, want := logs.String(), "feed: serial: boom\nfeed: serial: working again\n"; got != want {
			t.Errorf("log %q, want %q", got, want)
		}
	})
}

// partialWriter writes half of each burst and times out, as a slow port or
// one nobody reads does.
type partialWriter struct{}

func (partialWriter) Write(p []byte) (int, error) { return len(p) / 2, io.ErrShortWrite }

// A partial write is a slow port, not a lost one: it doesn't fail.
func TestSerialPartialWriteWorks(t *testing.T) {
	t.Parallel()

	synctest.Test(t, func(t *testing.T) {
		f, logs := newFeed(Outputs{Serial: partialWriter{}})
		f.SetLive(true)
		f.Update(flying())

		ctx, cancel := context.WithCancel(t.Context())
		defer cancel()

		go f.runSerial(ctx)

		for range 3 {
			f.send(flying(), time.Now())
			synctest.Wait()
			time.Sleep(outputSettle)
		}

		if st := f.Status(); st.Kind != Sending || logs.String() != "" {
			t.Errorf("status %+v, log %q", st, logs)
		}
	})
}

func TestNMEAQueueKeepsNewest(t *testing.T) {
	t.Parallel()

	synctest.Test(t, func(t *testing.T) {
		w := &serialWriter{block: true, release: make(chan struct{})}
		f, _ := newFeed(Outputs{Serial: w})

		ctx, cancel := context.WithCancel(t.Context())
		defer cancel()

		go f.runSerial(ctx)

		// The first fix is being written, and the write hangs; the
		// four queued after it replace each other.
		for lat := range 5 {
			f.send(position.Sample{Lat: float64(50 + lat), Lon: 12}, time.Now())
			synctest.Wait()
		}

		close(w.release)
		synctest.Wait()

		got := w.written()
		if len(got) != 2 || !strings.Contains(got[0], "5000.00000,N") ||
			!strings.Contains(got[1], "5400.00000,N") {
			t.Errorf("written %q, want the first fix, then the newest", got)
		}
	})
}

func TestRunWithStuckSerial(t *testing.T) {
	t.Parallel()

	synctest.Test(t, func(t *testing.T) {
		u := &fakeUDP{}
		w := &serialWriter{block: true, release: make(chan struct{})}
		f, _ := newFeed(Outputs{UDP: u, Name: "T", NoAttitude: true, Serial: w})
		f.SetTargets([]netip.AddrPort{target(1)})
		f.SetLive(true)
		f.Update(flying())

		ctx, cancel := context.WithCancel(t.Context())
		done := make(chan struct{})

		go func() {
			defer close(done)

			f.Run(ctx)
		}()

		// The serial write hangs from the first fix on, which must not
		// slow UDP down: a second brings a second's worth of packets.
		time.Sleep(time.Second)
		synctest.Wait()

		if n := len(u.kinds(target(1))); n != RateHz {
			t.Errorf("with a stuck serial port: %d XGPS in a second, want %d", n, RateHz)
		}

		cancel()
		close(w.release)
		<-done
	})
}

func TestStaleStandsStill(t *testing.T) {
	t.Parallel()

	synctest.Test(t, func(t *testing.T) {
		f, _ := newFeed(Outputs{})
		f.SetLive(true)
		f.Update(flying())

		time.Sleep(staleAfter / 2)

		if st := f.Status(); st.Kind != Sending || st.Sample.GSKnots != 100 {
			t.Errorf("fresh: %+v", st)
		}

		// Frames at the same position: a paused sim. After staleFrames
		// of them the position is still sent, standing still.
		for range staleFrames - 1 {
			time.Sleep(time.Second / RateHz)
			f.Update(flying())
		}

		if st := f.Status(); st.Sample.GSKnots != 100 {
			t.Errorf("after %d identical frames: %+v", staleFrames-1, st)
		}

		time.Sleep(time.Second / RateHz)
		f.Update(flying())

		if st := f.Status(); st.Kind != Sending || st.Sample.GSKnots != 0 || st.Sample.Lat != 51 {
			t.Errorf("stale: %+v", st)
		}

		// No frames at all, as from a hung sim: the same.
		f.Update(moved(flying()))
		time.Sleep(2 * staleAfter)

		if st := f.Status(); st.Kind != Sending || st.Sample.GSKnots != 0 {
			t.Errorf("no frames: %+v", st)
		}

		f.Update(moved(moved(flying())))

		if st := f.Status(); st.Sample.GSKnots != 100 {
			t.Errorf("a frame that moved: %+v", st)
		}
	})
}

func TestDropsNonFinite(t *testing.T) {
	t.Parallel()

	synctest.Test(t, func(t *testing.T) {
		f, _ := newFeed(Outputs{})
		f.SetLive(true)
		f.Update(flying())
		f.Update(position.Sample{Lat: math.NaN(), Lon: 12})
		f.Update(position.Sample{Lat: 51, Lon: 12, AltM: math.Inf(1)})

		if st := f.Status(); st.Kind != Sending || st.Sample != flying() {
			t.Errorf("after NaN and Inf samples: %+v", st)
		}
	})
}

func TestSerialFailsNamesPort(t *testing.T) {
	t.Parallel()

	if got := serialFails("COM11"); !strings.HasPrefix(got, "COM11: ") {
		t.Errorf("serial failure %q names no port", got)
	}
}

// The failures the tray shows fit its tooltip.
func TestFailuresFitTheTooltip(t *testing.T) {
	t.Parallel()

	for _, msg := range []string{
		udpFailsText, noTargetsText, serialFails(""), serialFails(`\\.\COM255`),
	} {
		if len(msg) > MaxErrLen {
			t.Errorf("%q: %d characters, room for %d", msg, len(msg), MaxErrLen)
		}
	}
}

// A new session judges the outputs afresh: a COM port that failed before
// doesn't show as failing until its next writes settle.
func TestSetLiveForgetsOutputErrors(t *testing.T) {
	t.Parallel()

	synctest.Test(t, func(t *testing.T) {
		w := &serialWriter{err: errBoom}
		f, _ := newFeed(Outputs{Serial: w})
		f.SetLive(true)
		f.Update(flying())

		ctx, cancel := context.WithCancel(t.Context())
		defer cancel()

		go f.runSerial(ctx)

		f.send(flying(), time.Now())
		synctest.Wait()
		time.Sleep(outputSettle)
		f.send(flying(), time.Now())
		synctest.Wait()

		if st := f.Status(); st.Kind != Failed {
			t.Fatalf("failing serial: %+v", st)
		}

		f.SetLive(false)
		f.SetLive(true)
		f.Update(flying())

		if st := f.Status(); st.Kind != Sending {
			t.Errorf("new session: %+v", st)
		}
	})
}

// sentLats returns the latitudes of the XGPS messages sent to dest.
func (u *fakeUDP) sentLats(t *testing.T, dest netip.AddrPort) []float64 {
	t.Helper()

	u.mu.Lock()
	defer u.mu.Unlock()

	var lats []float64

	for _, p := range u.got {
		fields := strings.Split(p.msg, ",")
		if p.to != dest || !strings.HasPrefix(p.msg, "XGPS") || len(fields) < 3 {
			continue
		}

		lat, err := strconv.ParseFloat(fields[2], 64)
		if err != nil {
			t.Fatal(err)
		}

		lats = append(lats, lat)
	}

	return lats
}

// Positions go out at RateHz whatever the sim's frame rate, moved on to
// the time they are sent: the aircraft moves the same distance between
// any two packets. Without that, at 45 fps the steps alternate between one
// frame's distance and two, at 25 fps every sixth packet repeats one.
func TestSendMovesPositionOn(t *testing.T) {
	t.Parallel()

	const (
		knots   = 100
		metres  = knots * position.KnotsToMetersPerSecond / RateHz // per packet
		degrees = 111195.0                                         // metres a degree north
	)

	for _, fps := range []int{25, 45, 60} {
		synctest.Test(t, func(t *testing.T) {
			u := &fakeUDP{}
			f, _ := newFeed(Outputs{UDP: u, Name: "T", NoAttitude: true})
			f.SetTargets([]netip.AddrPort{target(1)})
			f.SetLive(true)

			ctx, cancel := context.WithCancel(t.Context())
			done := make(chan struct{})

			go func() {
				defer close(done)

				f.Run(ctx)
			}()

			// Due north at a steady speed: the latitude is the time since
			// start.
			start := time.Now()
			perSecond := knots * position.KnotsToMetersPerSecond / degrees

			for range 2 * fps {
				lat := 51 + time.Since(start).Seconds()*perSecond
				f.Update(position.Sample{Lat: lat, Lon: 12, GSKnots: knots})
				time.Sleep(time.Second / time.Duration(fps))
			}

			// The sim hangs: the position is moved on by maxAhead at most,
			// and stays there once it counts as standing still, without
			// stepping back.
			last := f.Status().Sample.Lat

			time.Sleep(staleAfter + time.Second)
			synctest.Wait()
			cancel()
			<-done

			lats := u.sentLats(t, target(1))
			moving := lats[2 : 2*RateHz-2]

			for i := 1; i < len(moving); i++ {
				// 6 decimals are 0.11 m, which each end may be off by half.
				if step := (moving[i] - moving[i-1]) * degrees; math.Abs(step-metres) > 0.25 {
					t.Errorf("%d fps, packet %d: moved %.2f m, want %.2f", fps, i, step, metres)
				}
			}

			ahead := (lats[len(lats)-1] - last) * degrees

			want := maxAhead.Seconds() * knots * position.KnotsToMetersPerSecond
			if math.Abs(ahead-want) > 0.25 {
				t.Errorf("%d fps, hung sim: moved on %.2f m, want %.2f", fps, ahead, want)
			}
		})
	}
}

// Traffic goes to every UDP target once a second, only while positions
// are sent.
func TestRunSendsTraffic(t *testing.T) {
	t.Parallel()

	synctest.Test(t, func(t *testing.T) {
		u := &fakeUDP{}
		f, _ := newFeed(Outputs{UDP: u, Name: "T", NoAttitude: true})
		f.SetTargets([]netip.AddrPort{target(1), target(2)})
		f.SetTraffic([]position.Traffic{{Callsign: "A", ICAO: 1}, {Callsign: "B", ICAO: 2}})
		f.Update(flying())

		ctx, cancel := context.WithCancel(t.Context())
		done := make(chan struct{})

		go func() {
			defer close(done)

			f.Run(ctx)
		}()

		traffic := func(dest netip.AddrPort) int {
			return strings.Count(strings.Join(u.kinds(dest), ","), "XTRA")
		}

		// A position, but not live: nothing is sent.
		time.Sleep(time.Second + time.Millisecond)
		synctest.Wait()

		if n := traffic(target(1)); n != 0 {
			t.Errorf("not live: %d XTRAFFIC", n)
		}

		// SetLive drops the position from before.
		f.SetLive(true)
		f.Update(flying())
		time.Sleep(2 * time.Second)
		synctest.Wait()

		for _, to := range []netip.AddrPort{target(1), target(2)} {
			if n := traffic(to); n != 4 {
				t.Errorf("to %v: %d XTRAFFIC in two seconds, want 4", to, n)
			}
		}

		cancel()
		<-done
	})
}

// A TrafficFunc gets the position being sent, and is only called while
// positions are.
func TestRunTrafficFunc(t *testing.T) {
	t.Parallel()

	synctest.Test(t, func(t *testing.T) {
		u := &fakeUDP{}
		f, _ := newFeed(Outputs{UDP: u, Name: "T", NoAttitude: true})
		f.SetTargets([]netip.AddrPort{target(1)})

		var (
			lock sync.Mutex
			owns []position.Sample
		)

		f.SetTrafficFunc(func(own position.Sample, _ time.Time) []position.Traffic {
			lock.Lock()
			defer lock.Unlock()

			owns = append(owns, own)

			return []position.Traffic{{Callsign: "X", Lat: own.Lat, Lon: own.Lon}}
		})
		f.SetTraffic([]position.Traffic{{Callsign: "IGNORED"}})

		ctx, cancel := context.WithCancel(t.Context())
		done := make(chan struct{})

		go func() {
			defer close(done)

			f.Run(ctx)
		}()

		time.Sleep(time.Second + time.Millisecond)
		synctest.Wait()

		f.SetLive(true)
		f.Update(flying())
		time.Sleep(time.Second)
		synctest.Wait()

		cancel()
		<-done

		lock.Lock()
		defer lock.Unlock()

		// Moved on by up to maxAhead, as positions are sent.
		if len(owns) != 1 || math.Abs(owns[0].Lat-flying().Lat) > 0.001 {
			t.Errorf("called with %+v, want once with the position sent", owns)
		}

		for _, p := range u.got {
			if strings.HasPrefix(p.msg, "XTRAFFIC") && !strings.HasSuffix(p.msg, ",X") {
				t.Errorf("sent %q", p.msg)
			}
		}
	})
}

// Of more than MaxTraffic targets, the nearest are sent, also across the
// antimeridian.
func TestNearest(t *testing.T) {
	t.Parallel()

	own := position.Sample{Lat: 0.5, Lon: 179.99}

	traffic := make([]position.Traffic, 0, MaxTraffic+10)
	for i := range MaxTraffic + 10 {
		// Further east each, across 180°.
		lon := own.Lon + float64(i+1)*0.001
		if lon > 180 {
			lon -= 360
		}

		traffic = append(traffic, position.Traffic{
			ICAO: uint32(i), Lat: own.Lat, Lon: lon, Airborne: true,
		})
	}

	slices.Reverse(traffic)

	got := nearest(own, traffic)
	if len(got) != MaxTraffic {
		t.Fatalf("%d targets, want %d", len(got), MaxTraffic)
	}

	for i, x := range got {
		if x.ICAO != uint32(i) {
			t.Fatalf("target %d: ICAO %d, want the nearest first", i, x.ICAO)
		}
	}

	if few := traffic[:3]; len(nearest(own, few)) != 3 {
		t.Error("fewer than MaxTraffic not all sent")
	}
}

// At a busy airport, the aircraft on final 3 NM out comes before the ones
// parked next to the own, and one taxiing before those parked.
func TestNearestAirborneFirst(t *testing.T) {
	t.Parallel()

	own := position.Sample{Lat: 51.4, Lon: 12.2}

	traffic := make([]position.Traffic, 0, MaxTraffic+12)
	for i := range MaxTraffic + 10 {
		traffic = append(traffic, position.Traffic{
			Callsign: "PARKED", ICAO: uint32(i), Lat: own.Lat + float64(i)*1e-5, Lon: own.Lon,
		})
	}

	taxi := position.Traffic{Callsign: "TAXI", Lat: own.Lat + 0.01, Lon: own.Lon, GSKnots: 15}
	final := position.Traffic{
		Callsign: "FINAL", Lat: own.Lat + 0.05, Lon: own.Lon, GSKnots: 140, Airborne: true,
	}
	traffic = append(traffic, taxi, final)

	got := nearest(own, traffic)
	if len(got) != MaxTraffic || got[0].Callsign != "FINAL" || got[1].Callsign != "TAXI" {
		t.Fatalf("first %+v, %+v", got[0], got[1])
	}

	// The parked ones fill the rest, nearest first.
	if got[2].ICAO != 0 || got[MaxTraffic-1].ICAO != MaxTraffic-3 {
		t.Errorf("parked from %d to %d", got[2].ICAO, got[MaxTraffic-1].ICAO)
	}
}
