// Package serial opens a COM port for writing NMEA, configured as 8N1
// without flow control.
package serial

import (
	"errors"
	"fmt"
	"io"
	"math"
	"sync"
	"time"
)

var (
	// ErrBaud is returned for a baud rate a COM port can't be set to.
	ErrBaud = errors.New("baud rate must be at least 300, e.g. 115200")

	errClosed = errors.New("port closed")
)

// minBaud is the slowest rate accepted. Slower ones make a write's timeout,
// which covers its time on the wire, minutes long.
const minBaud = 300

// CheckBaud returns baud as the DCB's 32-bit BaudRate, or ErrBaud. Above
// the 32-bit range is rejected too, although ErrBaud doesn't say so: no
// COM port gets near it.
func CheckBaud(baud int) (uint32, error) {
	if baud < minBaud || uint64(baud) > math.MaxUint32 {
		return 0, fmt.Errorf("%w, not %d", ErrBaud, baud)
	}

	return uint32(baud), nil
}

// Working reports whether a write that wrote n bytes and returned err
// still shows a working port: one that worked, or timed out after some
// bytes, which is a slow port, or one nobody reads, not a lost one.
func Working(n int, err error) bool {
	return err == nil || errors.Is(err, io.ErrShortWrite) && n > 0
}

// Open returns a writer to the COM port name (e.g. "COM11") at baud, 8N1,
// a rate CheckBaud accepted. The port is opened by the first write, so that
// one that isn't there yet, e.g. a USB adapter still being set up after
// login, is picked up later. When a write fails, e.g. because the adapter
// went away while the PC slept, the port is closed and reopened by a later
// write, at most once per second; until then writes fail with the error.
func Open(name string, baud uint32) io.WriteCloser {
	return newReopening(func() (io.WriteCloser, error) { return openPort(name, baud) })
}

const (
	// reopenInterval is the least time between attempts to open a port.
	reopenInterval = time.Second
	// maxTimeouts is how many writes in a row may time out without
	// writing a byte before the port counts as lost: a driver wedged
	// after the PC resumed may only time out.
	maxTimeouts = 3
)

// reopening is a port that is opened on demand and reopened after it
// failed.
type reopening struct {
	open func() (io.WriteCloser, error)
	// w is the open port; nil before the first write and after a failure,
	// until err's retry at next.
	w        io.WriteCloser
	err      error
	next     time.Time
	timeouts int // writes in a row that timed out without writing a byte
	mu       sync.Mutex
	closed   bool
}

func newReopening(open func() (io.WriteCloser, error)) *reopening {
	return &reopening{open: open}
}

// Write writes b to the port. It doesn't hold the lock while the port
// writes, so that Close never waits for a write blocked in the driver.
func (r *reopening) Write(b []byte) (int, error) {
	w, err := r.port()
	if err != nil {
		return 0, err
	}

	n, err := w.Write(b)
	r.done(w, n, err)

	return n, err
}

// Close closes the port. A write in progress is cancelled, not waited for.
func (r *reopening) Close() error {
	r.mu.Lock()
	r.closed = true
	w := r.w
	r.w = nil
	r.mu.Unlock()

	if w == nil {
		return nil
	}

	return w.Close()
}

// port returns the open port, opening it when it's time to. It is only
// called by Write, which has one caller at a time. The port is opened
// without the lock: some drivers block in CreateFile for seconds, e.g. for
// a Bluetooth port whose device is off, and Close must not wait for that.
func (r *reopening) port() (io.WriteCloser, error) {
	r.mu.Lock()

	switch {
	case r.closed:
		r.mu.Unlock()

		return nil, errClosed
	case r.w != nil:
		w := r.w
		r.mu.Unlock()

		return w, nil
	case time.Now().Before(r.next):
		err := r.err
		r.mu.Unlock()

		return nil, err
	}

	r.mu.Unlock()

	w, err := r.open()

	r.mu.Lock()
	defer r.mu.Unlock()

	switch {
	case err != nil:
		r.err, r.next = err, time.Now().Add(reopenInterval)

		return nil, err
	case r.closed: // while opening
		_ = w.Close()

		return nil, errClosed
	}

	r.w, r.timeouts = w, 0

	return w, nil
}

// done records the outcome of a write to w. A failed write, or maxTimeouts
// in a row that wrote nothing, close the port for a reopen. A write that
// timed out after some bytes is a slow port, or one nobody reads, not a
// lost one.
func (r *reopening) done(w io.WriteCloser, n int, err error) {
	r.mu.Lock()
	defer r.mu.Unlock()

	if r.w != w { // closed meanwhile
		return
	}

	switch {
	case Working(n, err):
		r.timeouts = 0

		return
	case errors.Is(err, io.ErrShortWrite):
		r.timeouts++
		if r.timeouts < maxTimeouts {
			return
		}
	}

	_ = w.Close()
	r.w, r.err, r.next = nil, err, time.Now().Add(reopenInterval)
}

// idleCloser closes its port only while no write is in progress. Close
// during a write cancels the write and leaves the closing to it, so that a
// write blocked in the driver doesn't hold Close up, and the port isn't
// closed under it.
type idleCloser struct {
	port   io.WriteCloser
	cancel func()
	// wrote is closed when the write in progress returns; nil without one.
	wrote  chan struct{}
	mu     sync.Mutex
	closed bool
}

// cancelRetry is how often Close cancels a write again: a cancel that
// comes before the write reached the driver finds nothing to cancel.
const cancelRetry = 50 * time.Millisecond

func (c *idleCloser) Write(b []byte) (int, error) {
	c.mu.Lock()
	if c.closed {
		c.mu.Unlock()

		return 0, errClosed
	}

	wrote := make(chan struct{})
	c.wrote = wrote
	c.mu.Unlock()

	n, err := c.port.Write(b)

	c.mu.Lock()
	c.wrote = nil
	closed := c.closed
	c.mu.Unlock()
	close(wrote)

	if closed {
		_ = c.port.Close()
	}

	return n, err
}

func (c *idleCloser) Close() error {
	c.mu.Lock()
	defer c.mu.Unlock()

	if c.closed {
		return nil
	}

	c.closed = true
	if wrote := c.wrote; wrote != nil {
		go c.cancelUntil(wrote)

		return nil
	}

	return c.port.Close()
}

// cancelUntil cancels the write in progress, again every cancelRetry, until
// it returned. It cancels only under the lock while that write is still the
// one in progress: the handle is closed once it returned, and Windows reuses
// handle values, so a late cancel could hit another handle's I/O.
func (c *idleCloser) cancelUntil(wrote chan struct{}) {
	for {
		c.mu.Lock()
		if c.wrote != wrote {
			c.mu.Unlock()

			return
		}

		c.cancel()
		c.mu.Unlock()

		select {
		case <-wrote:
			return
		case <-time.After(cancelRetry):
		}
	}
}

const (
	// bitsPerByte on the wire at 8N1: start bit, 8 data bits, stop bit.
	bitsPerByte = 10
	msPerSecond = 1000
	// writeSlackMS is added to the time a write takes on the wire, so
	// that a port nobody reads (no flow control) never blocks for long.
	writeSlackMS = 100
)

// writeMultiplier returns the COMMTIMEOUTS write multiplier for baud, ms
// per byte rounded up, so that with writeSlackMS added the timeout covers a
// write's time on the wire.
func writeMultiplier(baud uint32) uint32 {
	const msPerBit = bitsPerByte * msPerSecond

	perByte := msPerBit / baud
	if msPerBit%baud != 0 {
		perByte++
	}

	return perByte
}
