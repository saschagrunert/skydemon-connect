package feed_test

import (
	"context"
	"errors"
	"net"
	"net/netip"
	"strings"
	"testing"
	"time"

	"github.com/saschagrunert/skydemon-connect/internal/feed"
	"github.com/saschagrunert/skydemon-connect/internal/position"
	"github.com/saschagrunert/skydemon-connect/internal/testutil"
)

var errFailed = errors.New("SimConnect.dll not found")

func TestStatus(t *testing.T) {
	t.Parallel()

	f := feed.New(feed.Outputs{}, testutil.Log(t))

	st := f.Status()
	if st.Kind != feed.Waiting || st.String() != "Waiting for MSFS…" {
		t.Errorf("new feed: %+v %q", st, st.String())
	}

	f.SetLive(true)
	f.Update(position.Sample{}) // 0°/0°: no position

	if f.Status().Kind != feed.Waiting {
		t.Error("0°/0° sample accepted")
	}

	f.Update(position.Sample{Lat: 51, Lon: 12, AltM: 762, GSKnots: 100, TrackTrue: 90})

	st = f.Status()
	if st.Kind != feed.Sending || st.String() != "30 Hz · 2500 ft · 100 kt · trk 090" {
		t.Errorf("sending: %+v %q", st, st.String())
	}

	f.Update(position.Sample{Lat: 51, Lon: 12, TrackTrue: 359.6})

	if s := f.Status(); !strings.HasSuffix(s.String(), "trk 000") {
		t.Errorf("track 359.6 should show as 000: %q", s.String())
	}

	f.SetError(errFailed)

	st = f.Status()
	if st.Kind != feed.Failed || !strings.Contains(st.String(), "SimConnect.dll") {
		t.Errorf("failed: %+v %q", st, st.String())
	}

	f.Update(position.Sample{Lat: 51, Lon: 12})

	if f.Status().Kind != feed.Sending {
		t.Error("an update should clear the error")
	}

	f.SetLive(false)

	if f.Status().Kind != feed.Waiting {
		t.Error("still sending after the session ended")
	}
}

// TestWhenToSend checks when the feed sends: only positions from the
// current sim session, and not while the sim is in its menu.
func TestWhenToSend(t *testing.T) {
	t.Parallel()

	pos := position.Sample{Lat: 51, Lon: 12}
	f := feed.New(feed.Outputs{}, testutil.Log(t))

	for _, step := range []struct {
		do   func()
		want bool
		what string
	}{
		{func() { f.Update(pos) }, false, "no sim connection"},
		{func() { f.SetLive(true) }, false, "a new session must not repeat an earlier position"},
		{func() { f.Update(pos) }, true, "connected with a position"},
		{func() { f.SetLive(true) }, true, "still connected"},
		{func() { f.Update(position.Sample{}) }, false, "back in the sim's menu"},
		{func() { f.Update(pos) }, true, "flying again"},
		{func() { f.SetLive(false) }, false, "session ended"},
		{func() { f.SetLive(true) }, false, "reconnected, still loading"},
	} {
		step.do()

		if got := f.Status().Kind == feed.Sending; got != step.want {
			t.Errorf("%s: sending = %v, want %v", step.what, got, step.want)
		}
	}
}

// TestRun is a smoke test over a real socket: targets set while running
// get the messages.
func TestRun(t *testing.T) {
	t.Parallel()

	conn, sender := testutil.ListenUDP(t), testutil.ListenUDP(t)

	f := feed.New(feed.Outputs{UDP: sender, Name: "T", NoAttitude: true}, testutil.Log(t))
	f.SetLive(true)
	f.Update(position.Sample{Lat: 51, Lon: 12})

	ctx, cancel := context.WithCancel(t.Context())
	done := make(chan struct{})

	go func() {
		defer close(done)

		f.Run(ctx)
	}()

	a, ok := conn.LocalAddr().(*net.UDPAddr)
	if !ok {
		t.Fatal("not a UDP addr")
	}

	f.SetTargets([]netip.AddrPort{a.AddrPort()})

	err := conn.SetReadDeadline(time.Now().Add(5 * time.Second))
	if err != nil {
		t.Fatal(err)
	}

	buf := make([]byte, 512)

	n, _, err := conn.ReadFrom(buf)
	if err != nil || !strings.HasPrefix(string(buf[:n]), "XGPST,") {
		t.Errorf("after SetTargets: %q %v", buf[:n], err)
	}

	cancel()
	<-done
}
