package main

import (
	"context"
	"errors"
	"log"
	"strings"
	"sync"
	"testing"
	"testing/synctest"
	"time"

	"github.com/saschagrunert/skydemon-connect/internal/feed"
	"github.com/saschagrunert/skydemon-connect/internal/simconnect"
	"github.com/saschagrunert/skydemon-connect/internal/testutil"
)

func TestOpenOutputs(t *testing.T) {
	t.Parallel()

	outs, err := openOutputs(flags(t, "-udp", "auto"), testutil.Log(t))
	if err != nil {
		t.Fatal(err)
	}
	defer outs.shutdown(&sync.WaitGroup{}, testutil.Log(t))

	if len(outs.targets) == 0 || outs.UDP == nil || outs.Serial != nil {
		t.Errorf("targets %v, udp %v, serial %v", outs.targets, outs.UDP, outs.Serial)
	}
}

func TestStartDemoEndToEnd(t *testing.T) {
	t.Parallel()

	conn := testutil.ListenUDP(t)
	c := flags(t, "-udp", conn.LocalAddr().String(), "-name", "T", "-demo",
		"-demo-lat", "51", "-demo-lon", "12")

	r, err := startFeed(t.Context(), c, input{sim: simconnect.Run},
		testutil.Log(t))
	if err != nil {
		t.Fatal(err)
	}

	err = conn.SetReadDeadline(time.Now().Add(2 * time.Second))
	if err != nil {
		t.Fatal(err)
	}

	buf := make([]byte, 512)

	n, _, err := conn.ReadFrom(buf)
	if err != nil || !strings.HasPrefix(string(buf[:n]), "XGPST,12.") {
		t.Errorf("first packet %q, %v", buf[:n], err)
	}

	if got := r.destinations(); len(got) != 1 || got[0] != "UDP: "+conn.LocalAddr().String() {
		t.Errorf("destinations %q", got)
	}

	start := time.Now()

	r.stop()

	if d := time.Since(start); d >= shutdownWait {
		t.Errorf("stop took %v", d)
	}
}

var errSim = errors.New("no sim")

func failingSim(_ context.Context, _ *log.Logger, h simconnect.Handlers) { h.Failed(errSim) }

func TestRunInputSimError(t *testing.T) {
	t.Parallel()

	f := feed.New(feed.Outputs{}, testutil.Log(t))
	runInput(t.Context(), f, flags(t), input{sim: failingSim},
		func() { t.Error("quit") }, testutil.Log(t))

	if st := f.Status(); st.Kind != feed.Failed || st.Err != errSim.Error() {
		t.Errorf("status %+v", st)
	}
}

// The sim's traffic is asked for unless -no-traffic, -test-traffic, which
// replaces it, or UDP is off, as only UDP carries it.
func TestRunInputTraffic(t *testing.T) {
	t.Parallel()

	for _, tc := range []struct {
		args []string
		want bool
	}{
		{nil, true},
		{[]string{"-no-traffic"}, false},
		{[]string{"-test-traffic"}, false},
		{[]string{"-udp=", "-com", "COM11"}, false},
	} {
		var got bool

		sim := func(_ context.Context, _ *log.Logger, h simconnect.Handlers) {
			got = h.Traffic != nil
		}

		f := feed.New(feed.Outputs{}, testutil.Log(t))
		runInput(t.Context(), f, flags(t, tc.args...), input{sim: sim}, func() {}, testutil.Log(t))

		if got != tc.want {
			t.Errorf("%q: traffic %v, want %v", tc.args, got, tc.want)
		}
	}
}

func TestSessionEndQuits(t *testing.T) {
	t.Parallel()

	for _, tc := range []struct{ auto, watched, want bool }{
		{false, false, false},
		{true, false, true},
		{true, true, false}, // MSFS's own exit ends the app
	} {
		if got := sessionEndQuits(tc.auto, tc.watched); got != tc.want {
			t.Errorf("sessionEndQuits(auto %v, watched %v) = %v", tc.auto, tc.watched, got)
		}
	}
}

func TestStartFeedQuit(t *testing.T) {
	t.Parallel()

	sim := func(ctx context.Context, _ *log.Logger, h simconnect.Handlers) {
		h.Ended()
		<-ctx.Done()
	}

	conn := testutil.ListenUDP(t)

	for _, quits := range []bool{false, true} {
		synctest.Test(t, func(t *testing.T) {
			c := flags(t, "-udp", conn.LocalAddr().String())

			r, err := startFeed(
				t.Context(),
				c,
				input{sim: sim, quitOnSessionEnd: quits},
				testutil.Log(t),
			)
			if err != nil {
				t.Fatal(err)
			}
			defer r.stop()

			synctest.Wait()

			select {
			case <-r.quit:
				if !quits {
					t.Error("quit when the session ended, without quitOnSessionEnd")
				}
			default:
				if quits {
					t.Error("no quit when the session ended, with quitOnSessionEnd")
				}
			}
		})
	}
}
