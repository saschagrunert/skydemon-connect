package main

import (
	"context"
	"errors"
	"log"
	"net/netip"
	"strings"
	"sync"
	"testing"
	"testing/synctest"
	"time"

	"github.com/saschagrunert/skydemon-connect/internal/feed"
	"github.com/saschagrunert/skydemon-connect/internal/simconnect"
	"github.com/saschagrunert/skydemon-connect/internal/targets"
	"github.com/saschagrunert/skydemon-connect/internal/testutil"
)

func TestOpenOutputs(t *testing.T) {
	t.Parallel()

	outs, err := openOutputs(flags(t, "-udp", "auto"), testutil.Log(t))
	if err != nil {
		t.Fatal(err)
	}
	defer outs.shutdown(&sync.WaitGroup{}, testutil.Log(t))

	if len(outs.targets) == 0 || outs.conn == nil || outs.port != nil {
		t.Errorf("targets %v, udp %v, serial %v", outs.targets, outs.conn, outs.port)
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
	runInput(t.Context(), f, &config{}, input{sim: failingSim},
		func() { t.Error("quit") }, testutil.Log(t))

	if st := f.Status(); st.Kind != feed.Failed || st.Err != errSim.Error() {
		t.Errorf("status %+v", st)
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

func TestRefreshTargets(t *testing.T) {
	t.Parallel()

	synctest.Test(t, func(t *testing.T) {
		a := netip.MustParseAddrPort("127.0.0.1:1")
		b := netip.MustParseAddrPort("127.0.0.1:2")
		calls := 0
		resolve := func(context.Context) targets.List {
			calls++
			if calls < 3 {
				return targets.List{a}
			}

			return targets.List{a, b}
		}

		var sets [][]netip.AddrPort

		set := func(l []netip.AddrPort) { sets = append(sets, l) }

		ctx, cancel := context.WithCancel(t.Context())
		done := make(chan struct{})

		go func() {
			defer close(done)

			refreshTargets(ctx, targetRefresh, resolve, set, targets.List{a}, testutil.Log(t))
		}()

		time.Sleep(5*targetRefresh + time.Second)
		synctest.Wait()
		cancel()
		<-done

		// Once at the start, then every interval.
		if calls != 6 || len(sets) != 1 || len(sets[0]) != 2 {
			t.Errorf("%d resolves, sets %v: want 6 and one change to [a b]", calls, sets)
		}
	})
}
