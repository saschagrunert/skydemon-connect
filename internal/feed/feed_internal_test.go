package feed

import (
	"context"
	"errors"
	"io"
	"log"
	"math"
	"net/netip"
	"slices"
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

		// Frames at the same position for longer: a paused sim. The
		// position is still sent, standing still.
		for range 2 * RateHz * staleAfter / time.Second {
			time.Sleep(time.Second / RateHz)
			f.Update(flying())
		}

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

	f, _ := newFeed(Outputs{})
	f.SetLive(true)
	f.Update(flying())
	f.Update(position.Sample{Lat: math.NaN(), Lon: 12})
	f.Update(position.Sample{Lat: 51, Lon: 12, AltM: math.Inf(1)})

	if st := f.Status(); st.Kind != Sending || st.Sample != flying() {
		t.Errorf("after NaN and Inf samples: %+v", st)
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

	if got := serialFails("COM11"); !strings.HasPrefix(got, "COM11: ") {
		t.Errorf("serial failure %q names no port", got)
	}
}
