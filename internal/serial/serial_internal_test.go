package serial

import (
	"errors"
	"io"
	"math"
	"sync"
	"sync/atomic"
	"testing"
	"testing/synctest"
	"time"
)

func TestWriteTimeouts(t *testing.T) {
	t.Parallel()

	for _, tc := range []struct {
		baud uint32
		want uint32
	}{
		{4800, 3},   // 2.08 ms per byte
		{9600, 2},   // 1.04
		{19200, 1},  // 0.52
		{115200, 1}, // 0.09
		{1, 10000},
		{math.MaxUint32, 1},
	} {
		perByte, constant := writeTimeouts(tc.baud)
		if perByte != tc.want || constant != writeSlackMS {
			t.Errorf("%d baud: %d ms/byte + %d, want %d + %d",
				tc.baud, perByte, constant, tc.want, writeSlackMS)
		}
	}

	// A full NMEA fix (about 206 bytes) at 9600 baud takes about 215 ms
	// on the wire: the timeout must cover it.
	perByte, constant := writeTimeouts(9600)
	if total := 206*perByte + constant; total < 215 {
		t.Errorf("timeout %d ms is shorter than the write", total)
	}
}

func TestCheckBaud(t *testing.T) {
	t.Parallel()

	for _, tc := range []struct {
		baud int
		want uint32
		ok   bool
	}{
		{minBaud, minBaud, true},
		{115200, 115200, true},
		{math.MaxUint32, math.MaxUint32, true},
		{minBaud - 1, 0, false},
		{0, 0, false},
		{-1, 0, false},
		{math.MaxUint32 + 1, 0, false},
	} {
		got, err := CheckBaud(tc.baud)
		if (err == nil) != tc.ok || got != tc.want || (err != nil && !errors.Is(err, ErrBaud)) {
			t.Errorf("CheckBaud(%d) = %d, %v", tc.baud, got, err)
		}
	}
}

var errGone = errors.New("device gone")

// fakePort fails writes while broken, and writes nothing, timing out,
// while stuck.
type fakePort struct {
	broken, stuck *bool
	closed        bool
	writes        int
}

func (p *fakePort) Write(b []byte) (int, error) {
	switch {
	case *p.broken:
		return 0, errGone
	case *p.stuck:
		return 0, io.ErrShortWrite
	}

	p.writes++

	return len(b), nil
}

func (p *fakePort) Close() error {
	p.closed = true

	return nil
}

// ports hands out fakePorts, or errGone while broken.
type ports struct {
	opened        []*fakePort
	broken, stuck bool
}

func (fake *ports) open() (io.WriteCloser, error) {
	if fake.broken {
		return nil, errGone
	}

	p := &fakePort{broken: &fake.broken, stuck: &fake.stuck}
	fake.opened = append(fake.opened, p)

	return p, nil
}

func TestReopening(t *testing.T) {
	t.Parallel()

	synctest.Test(t, func(t *testing.T) {
		fake := &ports{broken: true}
		r := newReopening(fake.open)

		write := func() error {
			_, err := r.Write([]byte("$GPRMC\r\n"))

			return err
		}

		// A port that isn't there yet is opened by a later write.
		err := write()
		if !errors.Is(err, errGone) || len(fake.opened) != 0 {
			t.Fatalf("first write: %v", err)
		}

		fake.broken = false

		time.Sleep(reopenInterval)

		err = write()
		if err != nil || len(fake.opened) != 1 || fake.opened[0].writes != 1 {
			t.Fatalf("write once the port is there: %v", err)
		}

		fake.broken = true

		err = write()
		if !errors.Is(err, errGone) || !fake.opened[0].closed {
			t.Errorf("failed write: %v, closed %v", err, fake.opened[0].closed)
		}

		// Within the interval no reopen is tried, and the error stays.
		fake.broken = false

		time.Sleep(reopenInterval / 2)

		err = write()
		if !errors.Is(err, errGone) || len(fake.opened) != 1 {
			t.Errorf("write before the interval: %v, %d opens", err, len(fake.opened))
		}

		// After it, the port is reopened and writes work again.
		time.Sleep(reopenInterval)

		err = write()
		if err != nil || len(fake.opened) != 2 || fake.opened[1].writes != 1 {
			t.Errorf("write after the interval: %v, %d opens", err, len(fake.opened))
		}

		err = r.Close()
		if err != nil || !fake.opened[1].closed {
			t.Fatalf("close: %v", err)
		}

		time.Sleep(reopenInterval)

		err = write()
		if !errors.Is(err, errClosed) || len(fake.opened) != 2 {
			t.Errorf("write after Close: %v, %d opens", err, len(fake.opened))
		}
	})
}

// Close doesn't wait for an open that blocks, as CreateFile can for a
// Bluetooth port whose device is off; the port opened meanwhile is closed.
func TestReopeningCloseWhileOpening(t *testing.T) {
	t.Parallel()

	synctest.Test(t, func(t *testing.T) {
		release := make(chan struct{})
		port := &fakePort{broken: new(bool), stuck: new(bool)}
		r := newReopening(func() (io.WriteCloser, error) {
			<-release

			return port, nil
		})

		done := make(chan error)

		go func() {
			_, err := r.Write([]byte("$GPRMC\r\n"))
			done <- err
		}()

		synctest.Wait() // the write is opening the port

		err := r.Close()
		if err != nil {
			t.Fatal(err)
		}

		close(release)

		err = <-done
		if !errors.Is(err, errClosed) || !port.closed || port.writes != 0 {
			t.Errorf("write %v, port closed %v, %d writes", err, port.closed, port.writes)
		}
	})
}

// Writes that time out without writing a byte mean a lost port, after
// maxTimeouts in a row; one that wrote some bytes is just a slow port.
func TestReopeningTimeouts(t *testing.T) {
	t.Parallel()

	synctest.Test(t, func(t *testing.T) {
		fake := &ports{}
		r := newReopening(fake.open)
		write := func() error {
			_, err := r.Write([]byte("abc"))

			return err
		}

		_ = write()
		fake.stuck = true

		for i := range maxTimeouts - 1 {
			err := write()
			if !errors.Is(err, io.ErrShortWrite) || fake.opened[0].closed {
				t.Fatalf("timeout %d: %v, closed %v", i+1, err, fake.opened[0].closed)
			}
		}

		_ = write()

		if !fake.opened[0].closed {
			t.Fatalf("%d timeouts in a row: port not closed", maxTimeouts)
		}

		fake.stuck = false

		time.Sleep(reopenInterval)

		err := write()
		if err != nil || len(fake.opened) != 2 {
			t.Errorf("reopen: %v, %d opens", err, len(fake.opened))
		}
	})
}

// A short write is a timeout of a port nobody reads: the port stays open.
func TestReopeningKeepsPortOnShortWrite(t *testing.T) {
	t.Parallel()

	p := &shortPort{}
	opens := 0
	r := newReopening(func() (io.WriteCloser, error) {
		opens++

		return p, nil
	})

	for range 2 * maxTimeouts {
		_, err := r.Write([]byte("abc"))
		if !errors.Is(err, io.ErrShortWrite) {
			t.Fatalf("short write: %v", err)
		}
	}

	if p.closed || opens != 1 {
		t.Errorf("closed %v, %d opens", p.closed, opens)
	}
}

type shortPort struct{ closed bool }

func (*shortPort) Write(b []byte) (int, error) { return len(b) - 1, io.ErrShortWrite }

func (p *shortPort) Close() error {
	p.closed = true

	return nil
}

// blockingPort's writes block until cancelled, as one to a wedged driver.
type blockingPort struct {
	writing   chan struct{}
	cancelled chan struct{}
	closed    chan struct{}
}

func (p *blockingPort) Write([]byte) (int, error) {
	close(p.writing)
	<-p.cancelled

	return 0, errGone
}

func (p *blockingPort) Close() error {
	close(p.closed) // panics if closed twice

	return nil
}

// Close doesn't wait for a write blocked in the driver: it cancels it, and
// the port is closed once the write returned, not under it.
func TestCloseDuringBlockedWrite(t *testing.T) {
	t.Parallel()

	p := &blockingPort{
		writing:   make(chan struct{}),
		cancelled: make(chan struct{}),
		closed:    make(chan struct{}),
	}
	port := &idleCloser{port: p, cancel: sync.OnceFunc(func() { close(p.cancelled) })}
	r := newReopening(func() (io.WriteCloser, error) { return port, nil })
	done := make(chan error)

	go func() {
		_, err := r.Write([]byte("abc"))
		done <- err
	}()

	<-p.writing

	err := r.Close()
	if err != nil {
		t.Fatal(err)
	}

	err = <-done
	if !errors.Is(err, errGone) {
		t.Errorf("cancelled write: %v", err)
	}

	// The write closed the port before it returned.
	select {
	case <-p.closed:
	default:
		t.Error("port not closed after the cancelled write")
	}

	// Closing again, or writing, doesn't touch the closed port.
	_ = port.Close()

	_, err = port.Write([]byte("x"))
	if !errors.Is(err, errClosed) {
		t.Errorf("write after close: %v", err)
	}
}

// A cancel that comes before the write reached the driver finds nothing
// to cancel: Close cancels again until the write returns.
func TestCloseCancelsAgain(t *testing.T) {
	t.Parallel()

	synctest.Test(t, func(t *testing.T) {
		var cancels atomic.Int32

		cancelled := make(chan struct{})
		p := &blockingPort{
			writing:   make(chan struct{}),
			cancelled: cancelled,
			closed:    make(chan struct{}),
		}
		missFirst := sync.OnceFunc(func() { close(cancelled) })
		port := &idleCloser{port: p, cancel: func() {
			if cancels.Add(1) > 1 { // the first one is missed
				missFirst()
			}
		}}

		done := make(chan error)

		go func() {
			_, err := port.Write([]byte("abc"))
			done <- err
		}()

		<-p.writing

		err := port.Close()
		if err != nil {
			t.Fatal(err)
		}

		err = <-done
		if !errors.Is(err, errGone) || cancels.Load() < 2 {
			t.Errorf("write %v after %d cancels", err, cancels.Load())
		}

		synctest.Wait() // the retrying goroutine has ended
	})
}

// slowPort's write takes exactly cancelRetry and ignores cancels, so that
// the write returning and Close's next cancel are due at the same moment.
type slowPort struct {
	mu     sync.Mutex
	closed bool
}

func (p *slowPort) Write(b []byte) (int, error) {
	time.Sleep(cancelRetry)

	return len(b), nil
}

func (p *slowPort) Close() error {
	p.mu.Lock()
	defer p.mu.Unlock()

	p.closed = true

	return nil
}

// A cancel never comes after the write returned and closed the handle:
// Windows reuses handle values, so it could cancel another handle's I/O.
func TestNoCancelAfterClose(t *testing.T) {
	t.Parallel()

	for range 200 {
		synctest.Test(t, func(t *testing.T) {
			p := &slowPort{}
			c := &idleCloser{port: p, cancel: func() {
				p.mu.Lock()
				defer p.mu.Unlock()

				if p.closed {
					t.Error("cancel after the port was closed")
				}
			}}

			done := make(chan struct{})

			go func() {
				defer close(done)

				_, _ = c.Write([]byte("abc"))
			}()

			synctest.Wait() // the write is in progress

			_ = c.Close()

			<-done
			time.Sleep(2 * cancelRetry)
			synctest.Wait()
		})
	}
}

func TestIdleCloserClosesWhenIdle(t *testing.T) {
	t.Parallel()

	p := &shortPort{}
	c := &idleCloser{port: p, cancel: func() { t.Error("nothing to cancel") }}

	err := c.Close()
	if err != nil || !p.closed {
		t.Errorf("close: %v, closed %v", err, p.closed)
	}
}
