package simconnect

import (
	"context"
	"errors"
	"fmt"
	"log"
	"time"

	"github.com/saschagrunert/skydemon-connect/internal/position"
	"github.com/saschagrunert/skydemon-connect/internal/report"
)

// clientName is how SimConnect lists this client.
const clientName = "SkyDemonConnect"

// The connection loop's timing.
const (
	// retryInterval is the pause between sessions.
	retryInterval = 3 * time.Second
	// probeInterval: while no messages arrive (a paused sim, or a crashed
	// one, which sends no QUIT), the data request is re-issued this
	// often. It fails once the pipe is gone, so a crash is noticed within
	// about this time, during which the last position is still sent. A
	// sim that hangs with its pipe open looks paused until it closes.
	probeInterval = 2 * time.Second
	// openTimeout bounds the wait for the sim's answer to a connection.
	openTimeout = 30 * time.Second
	// maxWait is the longest wait for a message, which bounds how quickly
	// the loop notices that its context is done.
	maxWait = 100 * time.Millisecond
)

var (
	errOpen    = errors.New("open")
	errQuit    = errors.New("MSFS quit")
	errNoOpen  = errors.New("MSFS did not answer the connection")
	errWaiting = errors.New("waiting for MSFS")
)

// conn is one open SimConnect connection: the DLL on Windows, a fake in
// the tests.
type conn interface {
	addToDataDefinition(name, unit string) error
	// addTrafficVar adds a variable to the traffic definition.
	addTrafficVar(name, unit string, datatype uint32) error
	// requestTraffic asks once for the objects of objectType within
	// radius metres, answered with request.
	requestTraffic(request, objectType, radius uint32) error
	// subscribe asks for the system event name, sent with the event ID.
	subscribe(event uint32, name string) error
	requestData() error
	// nextDispatch returns the next message, or nil when the queue is
	// empty. The slice is only valid until the next call.
	nextDispatch() []byte
	// wait blocks until a message may be available, at most for maxWait.
	wait()
	close()
}

// opener opens a connection to the sim. A *setupError is a problem the
// user must fix, reported through Handlers.Failed.
type opener func(name string) (conn, error)

// looper is the connection loop's state across sessions.
type looper struct {
	log  *log.Logger
	open opener
	h    Handlers
	rep  *report.Reporter
	// setup is the setup problem last passed to Failed, "" for none.
	setup string
	// live and connected are the states last passed to Live and
	// Connected.
	live, connected bool
	// traffic is whether the traffic last passed to Traffic had any.
	traffic bool
}

// loop connects to the sim and reconnects until ctx is done. Errors are
// logged when they change, so that a sim that is not running, or a broken
// setup, logs once and not every retry.
func loop(ctx context.Context, l *log.Logger, open opener, h Handlers) {
	state := &looper{log: l, open: open, h: h, rep: report.New(l)}

	for ctx.Err() == nil {
		connected, err := session(ctx, l, state.openConn, state.handlers())

		state.setLive(false)
		state.setConnected(false)
		state.clearTraffic()

		if ctx.Err() != nil {
			return
		}

		state.ended(connected, err)

		select {
		case <-ctx.Done():
		case <-time.After(retryInterval):
		}
	}
}

// handlers are the user's, with Live and Connected passed on only when
// they change.
func (lp *looper) handlers() Handlers {
	h := lp.h
	h.Live, h.Connected = lp.setLive, lp.setConnected

	if h.Traffic != nil {
		h.Traffic = lp.setTraffic
	}

	return h
}

func (lp *looper) setTraffic(t []position.Traffic) {
	lp.traffic = len(t) > 0
	lp.h.Traffic(t)
}

// clearTraffic tells Traffic that the session's traffic is gone, if it
// had any.
func (lp *looper) clearTraffic() {
	if lp.traffic {
		lp.setTraffic(nil)
	}
}

// openConn opens a connection. A setup problem is solved once one opens:
// say so then, not only when the session ends, which may be a whole
// flight later.
func (lp *looper) openConn(name string) (conn, error) {
	c, err := lp.open(name)
	if err == nil {
		lp.setSetup(nil)
	}

	return c, err
}

// ended handles the end of a session, which connected or not, and why.
func (lp *looper) ended(connected bool, err error) {
	if bad, isSetup := errors.AsType[*setupError](err); isSetup {
		lp.setSetup(bad.user)
	} else {
		lp.setSetup(nil)
	}

	if connected {
		lp.log.Printf("simconnect: session ended: %v", err)
		lp.h.Ended()

		// A fresh start: the next problem is logged, even if it is the
		// one from before this session. A new Reporter, not Forget, which
		// would log the "waiting for MSFS" before the session as reset.
		lp.rep = report.New(lp.log)
	} else {
		lp.rep.Report("simconnect", failure(err))
	}
}

func (lp *looper) setLive(live bool) {
	if live != lp.live {
		lp.live = live
		lp.h.Live(live)
	}
}

func (lp *looper) setConnected(connected bool) {
	if connected != lp.connected {
		lp.connected = connected
		lp.h.Connected(connected)
	}
}

// setSetup passes a setup problem, such as a missing DLL, to Failed when it
// starts, changes or ends (nil).
func (lp *looper) setSetup(err error) {
	msg := ""
	if err != nil {
		msg = err.Error()
	}

	if msg != lp.setup {
		lp.setup = msg
		lp.h.Failed(err)
	}
}

// failure describes why a session that never connected ended.
func failure(err error) error {
	switch {
	case errors.Is(err, errNoSim):
		return errWaiting
	case errors.Is(err, errOpen):
		// Not the usual "no sim running": say what it was.
		return fmt.Errorf("%w (%w)", errWaiting, err)
	}

	return err
}

// session runs one connection. The bool reports whether the sim answered.
func session(ctx context.Context, l *log.Logger, open opener, h Handlers) (bool, error) {
	c, err := open(clientName)
	if err != nil {
		if _, isSetup := errors.AsType[*setupError](err); isSetup {
			return false, err
		}

		return false, fmt.Errorf("%w: %w", errOpen, err)
	}
	defer c.close()

	for _, v := range simVars() {
		err = c.addToDataDefinition(v.name, v.unit)
		if err != nil {
			return false, fmt.Errorf("add %s: %w", v.name, err)
		}
	}

	// The Sim event tells a flight from the menus, where a sim that sends
	// no frames would otherwise look paused. Not verified against a real
	// MSFS 2024: should it not send it, the session is live from its OPEN
	// on.
	err = c.subscribe(eventSim, "Sim")
	if err != nil {
		return false, fmt.Errorf("subscribe Sim: %w", err)
	}

	err = c.requestData()
	if err != nil {
		return false, fmt.Errorf("request data: %w", err)
	}

	if h.Traffic != nil {
		err = defineTraffic(c)
		if err != nil {
			// Positions matter more: go on without traffic.
			l.Printf("simconnect: no traffic: %v", err)

			h.Traffic = nil
		}
	}

	return dispatch(ctx, c, &dispatcher{log: l, h: h})
}

// defineTraffic sets up the traffic definition.
func defineTraffic(c conn) error {
	for _, v := range trafficVars() {
		err := c.addTrafficVar(v.name, v.unit, v.datatype)
		if err != nil {
			return fmt.Errorf("add %s: %w", v.name, err)
		}
	}

	return nil
}

// trafficTick asks for traffic when it is time to, while connected.
func trafficTick(c conn, d *dispatcher, now time.Time) error {
	if d.h.Traffic == nil || !d.connected {
		return nil
	}

	changed, err := d.traffic.due(now, func(id, objectType uint32) error {
		return c.requestTraffic(id, objectType, trafficRadiusM)
	})

	if changed {
		d.h.Traffic(d.traffic.all())
	}

	if err != nil {
		return fmt.Errorf("request traffic: %w", err)
	}

	return nil
}

// dispatch handles messages until the session ends or ctx is done. The
// bool reports whether the sim answered.
func dispatch(ctx context.Context, c conn, d *dispatcher) (bool, error) {
	start := time.Now()
	lastMsg, lastProbe := start, start

	for ctx.Err() == nil {
		d.tick(time.Now())

		err := trafficTick(c, d, time.Now())
		if err != nil {
			return d.connected, fmt.Errorf("connection lost: %w", err)
		}

		buf := c.nextDispatch()
		if buf == nil {
			now := time.Now()

			if !d.connected && now.Sub(start) > openTimeout {
				return false, errNoOpen
			}

			// Probed before the sim answered too: one that dies before
			// its OPEN would otherwise be waited for until the timeout.
			if now.Sub(lastMsg) > probeInterval && now.Sub(lastProbe) > probeInterval {
				lastProbe = now

				err := c.requestData()
				if err != nil {
					return d.connected, fmt.Errorf("connection lost: %w", err)
				}
			}

			c.wait()

			continue
		}

		lastMsg = time.Now()

		if d.handle(buf) {
			// The sim quit; connected is false for a QUIT before OPEN.
			return d.connected, errQuit
		}
	}

	return d.connected, nil
}
