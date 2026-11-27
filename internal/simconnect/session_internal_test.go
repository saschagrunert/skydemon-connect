package simconnect

import (
	"context"
	"errors"
	"fmt"
	"log"
	"slices"
	"strings"
	"sync"
	"testing"
	"testing/synctest"
	"time"

	"github.com/saschagrunert/skydemon-connect/internal/position"
	"github.com/saschagrunert/skydemon-connect/internal/testutil"
)

var (
	errFake   = errors.New("fake")
	errDetail = errors.New("not in C:\\x")
)

// Header-only messages: dwSize 12, dwVersion 4, dwID.
func msgOpen() []byte { return []byte{12, 0, 0, 0, 4, 0, 0, 0, 2, 0, 0, 0} }

func msgQuit() []byte { return []byte{12, 0, 0, 0, 4, 0, 0, 0, 3, 0, 0, 0} }

// msgEvent is a SIMCONNECT_RECV_EVENT: dwSize 24, dwVersion 4, dwID 4,
// uGroupID 0xFFFFFFFF (none), uEventID, dwData.
func msgEvent(id, data byte) []byte {
	return []byte{
		24, 0, 0, 0, 4, 0, 0, 0, 4, 0, 0, 0,
		0xFF, 0xFF, 0xFF, 0xFF, id, 0, 0, 0, data, 0, 0, 0,
	}
}

// msgException is a SIMCONNECT_RECV_EXCEPTION: the header, dwException,
// dwSendID, dwIndex.
func msgException(code byte) []byte {
	return []byte{24, 0, 0, 0, 4, 0, 0, 0, 1, 0, 0, 0, code, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0}
}

// fakeConn replays queued messages; requestData fails from the nth call
// on when failRequestFrom is set, and calls onRequest when set. Its wait
// sleeps, which in a synctest bubble takes no real time.
type fakeConn struct {
	defineErr       error
	subscribeErr    error
	onRequest       func(n int)
	msgs            [][]byte
	events          []string
	failRequestFrom int
	requests        int
	closed          bool
}

func (c *fakeConn) addToDataDefinition(string, string) error { return c.defineErr }

func (c *fakeConn) subscribe(id uint32, name string) error {
	c.events = append(c.events, fmt.Sprintf("%s=%d", name, id))

	return c.subscribeErr
}

func (c *fakeConn) requestData() error {
	c.requests++
	if c.onRequest != nil {
		c.onRequest(c.requests)
	}

	if c.failRequestFrom > 0 && c.requests >= c.failRequestFrom {
		return errFake
	}

	return nil
}

func (c *fakeConn) nextDispatch() []byte {
	if len(c.msgs) == 0 {
		return nil
	}

	m := c.msgs[0]
	c.msgs = c.msgs[1:]

	return m
}

func (*fakeConn) wait() { time.Sleep(maxWait) }

func (c *fakeConn) close() { c.closed = true }

// recorder records the handler calls in order.
type recorder struct {
	mu     sync.Mutex
	events []string
	// connected records the Connected calls, apart from events.
	connected []bool
}

func (r *recorder) String() string {
	r.mu.Lock()
	defer r.mu.Unlock()

	return strings.Join(r.events, ",")
}

func (r *recorder) add(e string) {
	r.mu.Lock()
	r.events = append(r.events, e)
	r.mu.Unlock()
}

func (r *recorder) handlers() Handlers {
	return Handlers{
		Update: func(position.Sample) { r.add("update") },
		Live: func(l bool) {
			if l {
				r.add("live")
			} else {
				r.add("dead")
			}
		},
		Connected: func(c bool) {
			r.mu.Lock()
			r.connected = append(r.connected, c)
			r.mu.Unlock()
		},
		Ended: func() { r.add("ended") },
		Failed: func(err error) {
			if err != nil {
				r.add("failed: " + err.Error())
			} else {
				r.add("fixed")
			}
		},
	}
}

// opens returns an opener handing out conns in turn (nil: the open fails
// with errFake, an error: that error), which cancels ctx once all were
// used, and how often it was called.
func opens(t *testing.T, cancel context.CancelFunc, conns ...any) (opener, *int) {
	t.Helper()

	n := 0

	return func(name string) (conn, error) {
		if name != clientName {
			t.Errorf("client name %q", name)
		}

		n++
		if n > len(conns) {
			cancel()

			return nil, errFake
		}

		switch c := conns[n-1].(type) {
		case *fakeConn:
			return c, nil
		case error:
			return nil, c
		}

		return nil, errFake
	}, &n
}

// runLoop runs loop in a synctest bubble, with conns as for opens, until
// all were used, and returns the handler calls, the number of opens and
// the log.
func runLoop(t *testing.T, conns ...any) (string, int, string) {
	t.Helper()

	var (
		r     recorder
		out   testutil.Buffer
		count int
	)

	synctest.Test(t, func(t *testing.T) {
		ctx, cancel := context.WithTimeout(t.Context(), time.Hour)
		defer cancel()

		open, n := opens(t, cancel, conns...)
		loop(ctx, log.New(&out, "", 0), open, r.handlers())

		count = *n
	})

	for i, c := range conns {
		if c, ok := c.(*fakeConn); ok && !c.closed {
			t.Errorf("connection %d not closed", i)
		}
	}

	return r.String(), count, out.String()
}

func TestLoopQuit(t *testing.T) {
	t.Parallel()

	data := goldenData(t)
	got, n, _ := runLoop(t, &fakeConn{msgs: [][]byte{
		msgOpen(), msgEvent(eventSim, 1), data, data, msgQuit(),
	}})

	if want := "live,update,update,dead,ended"; got != want {
		t.Errorf("events %q, want %q", got, want)
	}

	if n != 2 {
		t.Errorf("%d opens, want a reconnect after the quit", n)
	}
}

func TestLoopQuitBeforeOpen(t *testing.T) {
	t.Parallel()

	// A QUIT without OPEN: the sim never answered, so no Ended.
	got, n, _ := runLoop(t, &fakeConn{msgs: [][]byte{msgQuit()}})

	if got != "" || n != 2 {
		t.Errorf("events %q, %d opens; want none and a retry", got, n)
	}
}

func TestLoopFlightEvents(t *testing.T) {
	t.Parallel()

	data := goldenData(t)
	c := &fakeConn{msgs: [][]byte{
		msgEvent(eventSim, 0),       // in the main menu, before OPEN
		msgOpen(),                   // connected, but not live
		msgEvent(eventSim, 1), data, // a flight
		msgEvent(eventSim, 1), // no change: no second Live(true)
		msgEvent(eventSim, 0), // back to the menu
		msgEvent(99, 1),       // not ours: ignored
		msgQuit(),
	}}
	got, _, out := runLoop(t, c)

	if want := "live,update,dead,ended"; got != want {
		t.Errorf("events %q, want %q", got, want)
	}

	if subs := strings.Join(c.events, " "); subs != "Sim=1" {
		t.Errorf("subscriptions %q", subs)
	}

	if !strings.Contains(out, "simconnect: no flight running (menu or loading)") {
		t.Errorf("log %q", out)
	}
}

func TestLoopNoOpenAnswer(t *testing.T) {
	t.Parallel()

	// The sim never sends OPEN: no Live, no Ended, but a retry after the
	// open timeout.
	got, n, out := runLoop(t, &fakeConn{}, &fakeConn{})

	if got != "" || n != 3 {
		t.Errorf("events %q, %d opens; want none and 3", got, n)
	}

	if strings.Count(out, errNoOpen.Error()) != 1 {
		t.Errorf("log %q", out)
	}
}

func TestLoopProbe(t *testing.T) {
	t.Parallel()

	// The sim answers and then goes silent; the second request (the first
	// probe) fails as the pipe is gone.
	c := &fakeConn{msgs: [][]byte{msgOpen()}, failRequestFrom: 2}
	got, _, _ := runLoop(t, c)

	if want := "live,dead,ended"; got != want || c.requests != 2 {
		t.Errorf("events %q (want %q), %d requests (want 2)", got, want, c.requests)
	}
}

func TestLoopProbeBeforeOpen(t *testing.T) {
	t.Parallel()

	// A sim that dies before its OPEN: the probe ends the session without
	// Ended, before the open timeout.
	c := &fakeConn{failRequestFrom: 2}
	got, _, _ := runLoop(t, c)

	if got != "" || c.requests != 2 {
		t.Errorf("events %q, %d requests", got, c.requests)
	}
}

func TestLoopProbeKeepsPausedSim(t *testing.T) {
	t.Parallel()

	synctest.Test(t, func(t *testing.T) {
		ctx, cancel := context.WithTimeout(t.Context(), time.Hour)
		defer cancel()

		// The sim answers and then sends nothing, as when paused: probes
		// keep the session up. Stop at the third, 10 s in.
		r := &recorder{}
		c := &fakeConn{msgs: [][]byte{msgOpen()}, onRequest: func(n int) {
			if n == 3 {
				cancel()
			}
		}}

		start := time.Now()
		d := &dispatcher{log: testutil.Log(t), h: r.handlers()}

		connected, err := dispatch(ctx, c, d)
		if !connected || err != nil {
			t.Errorf("connected %v, err %v", connected, err)
		}

		if got := r.String(); got != "live" {
			t.Errorf("events %q, want the session to stay live", got)
		}

		if c.requests != 3 || time.Since(start) < 2*probeInterval {
			t.Errorf("%d data requests in %v, want probes every %v",
				c.requests, time.Since(start), probeInterval)
		}
	})
}

func TestLoopLogsErrorsOnce(t *testing.T) {
	t.Parallel()

	bad := func() *fakeConn { return &fakeConn{defineErr: errFake} }
	got, _, out := runLoop(t, nil, nil, bad(), bad(), nil)

	if got != "" {
		t.Errorf("events %q", got)
	}

	if n := strings.Count(out, "waiting for MSFS (open: fake)"); n != 2 {
		t.Errorf("%d waiting messages, want 2:\n%s", n, out)
	}

	if n := strings.Count(out, "add PLANE LATITUDE: fake"); n != 1 {
		t.Errorf("%d define errors, want 1:\n%s", n, out)
	}
}

func TestLoopWaitingForSim(t *testing.T) {
	t.Parallel()

	_, _, out := runLoop(t, errNoSim, errNoSim, errNoSim)

	if strings.Count(out, "simconnect: waiting for MSFS\n") != 1 {
		t.Errorf("want one plain waiting message for E_FAIL:\n%s", out)
	}
}

// A missing SimConnect.dll is reported once through Failed, and cleared
// once it is found, without a restart.
func TestLoopSetupFailure(t *testing.T) {
	t.Parallel()

	missing := &setupError{user: errNotFound, detail: errDetail}
	data := goldenData(t)
	got, n, out := runLoop(t, missing, missing, errNoSim,
		&fakeConn{msgs: [][]byte{msgOpen(), msgEvent(eventSim, 1), data, msgQuit()}})

	want := "failed: " + errNotFound.Error() + ",fixed,live,update,dead,ended"
	if got != want || n != 5 {
		t.Errorf("events %q (want %q), %d opens", got, want, n)
	}

	if strings.Count(out, "not in C:\\x") != 1 {
		t.Errorf("the detail is logged once:\n%s", out)
	}
}

// A fixed setup is reported as soon as a connection opens, also when the
// session then sits in the menu for long, without positions.
func TestLoopSetupFixedOnOpen(t *testing.T) {
	t.Parallel()

	missing := &setupError{user: errNotFound, detail: errDetail}
	got, _, _ := runLoop(t, missing,
		&fakeConn{msgs: [][]byte{msgOpen(), msgEvent(eventSim, 1), msgQuit()}})

	// Fixed as soon as the DLL opens, before the flight: not only once the
	// session ended, which would show the error all flight long.
	want := "failed: " + errNotFound.Error() + ",fixed,live,dead,ended"
	if got != want {
		t.Errorf("events %q, want %q", got, want)
	}
}

// The setup calls after connecting fail like the sim going away: logged,
// and retried, without a live session.
func TestLoopSetupCallErrors(t *testing.T) {
	t.Parallel()

	for _, tc := range []struct {
		conn *fakeConn
		want string
	}{
		{&fakeConn{subscribeErr: errFake}, "subscribe Sim: fake"},
		{&fakeConn{failRequestFrom: 1}, "request data: fake"},
	} {
		got, _, out := runLoop(t, tc.conn)
		if !strings.Contains(out, tc.want) || strings.Contains(got, "live") {
			t.Errorf("events %q, log %q, want %q", got, out, tc.want)
		}
	}
}

// Connected follows the session, apart from whether a flight runs: true at
// OPEN, also in the menu, and false when the session ends.
func TestLoopConnected(t *testing.T) {
	t.Parallel()

	synctest.Test(t, func(t *testing.T) {
		ctx, cancel := context.WithCancel(t.Context())
		r := &recorder{}
		conns := []*fakeConn{
			{msgs: [][]byte{msgEvent(eventSim, 0), msgOpen(), msgQuit()}}, // in the menu
			{}, // no answer
		}
		n := 0
		open := func(string) (conn, error) {
			if n == len(conns) {
				cancel()

				return nil, errFake
			}

			n++

			return conns[n-1], nil
		}

		loop(ctx, testutil.Log(t), open, r.handlers())

		events := r.String()

		r.mu.Lock()
		connected := slices.Clone(r.connected)
		r.mu.Unlock()

		if !slices.Equal(connected, []bool{true, false}) || strings.Contains(events, "live") {
			t.Errorf("connected %v, events %q", connected, events)
		}
	})
}

func TestLoopExceptionsLoggedOnce(t *testing.T) {
	t.Parallel()

	_, _, out := runLoop(t, &fakeConn{msgs: [][]byte{
		msgOpen(), msgException(7), msgException(7), msgException(9), msgException(7), msgQuit(),
	}})

	if strings.Count(out, "exception 7") != 1 || strings.Count(out, "exception 9") != 1 {
		t.Errorf("log %q", out)
	}
}

func TestLoopCancel(t *testing.T) {
	t.Parallel()

	synctest.Test(t, func(t *testing.T) {
		ctx, cancel := context.WithCancel(t.Context())
		c := &fakeConn{msgs: [][]byte{msgOpen()}}
		r := &recorder{}
		done := make(chan struct{})

		go func() {
			loop(ctx, testutil.Log(t),
				func(string) (conn, error) { return c, nil }, r.handlers())
			close(done)
		}()

		// The session is up and waiting for messages.
		time.Sleep(time.Second)
		synctest.Wait()

		cancel()
		<-done

		if got, want := r.String(), "live,dead"; got != want || !c.closed {
			t.Errorf("events %q (want %q), closed %v", got, want, c.closed)
		}
	})
}

// Without a Sim event, the session goes live simGrace after OPEN; with
// one, as it says, at once.
func TestLoopSimGrace(t *testing.T) {
	t.Parallel()

	data := goldenData(t)

	for _, tc := range []struct {
		name string
		msgs [][]byte
		want string
	}{
		{"no Sim event", [][]byte{msgOpen(), data}, "update,live"},
		{"flight running", [][]byte{msgOpen(), msgEvent(eventSim, 1), data}, "live,update"},
		// The dispatcher passes the Sim event on; the loop drops a Live
		// that doesn't change anything.
		{"in a menu", [][]byte{msgOpen(), msgEvent(eventSim, 0), data}, "dead,update"},
		{"Sim before OPEN", [][]byte{msgEvent(eventSim, 1), msgOpen(), data}, "live,update"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			synctest.Test(t, func(t *testing.T) {
				r := &recorder{}
				ctx, cancel := context.WithCancel(t.Context())
				done := make(chan struct{})

				go func() {
					defer close(done)

					_, _ = dispatch(ctx, &fakeConn{msgs: tc.msgs},
						&dispatcher{log: testutil.Log(t), h: r.handlers()})
				}()

				time.Sleep(2 * simGrace)
				synctest.Wait()
				cancel()
				<-done

				if got := r.String(); got != tc.want {
					t.Errorf("events %q, want %q", got, tc.want)
				}
			})
		})
	}
}

// Exceptions are logged once per code and call, data for another request
// is ignored.
func TestExceptionPerCall(t *testing.T) {
	t.Parallel()

	var out testutil.Buffer

	r := &recorder{}
	d := &dispatcher{log: log.New(&out, "", 0), h: r.handlers()}

	again := msgException(7)
	again[sendIDOffset]++ // the same call made again, e.g. the next probe

	other := msgException(7)
	other[indexOffset]++ // another argument rejected the same way

	for _, b := range [][]byte{msgException(7), again, other} {
		d.handle(b)
	}

	if n := strings.Count(out.String(), "exception 7"); n != 2 {
		t.Errorf("%d exception lines, want one per argument:\n%s", n, out.String())
	}

	data := goldenData(t)
	data[requestIDOffset] = requestID + 1
	d.handle(data)

	if got := r.String(); got != "" {
		t.Errorf("data for another request was used: %q", got)
	}
}
