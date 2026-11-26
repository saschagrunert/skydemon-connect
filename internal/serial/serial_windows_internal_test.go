//go:build windows

package serial

import (
	"syscall"
	"testing"
	"time"
)

// Every Win32 function is looked up lazily: a typo in a name would only
// show when it is first called.
func TestProcsExist(t *testing.T) {
	t.Parallel()

	for _, p := range []*syscall.LazyProc{
		procGetCommState, procSetCommState, procSetCommTimeouts, procCancelIoEx,
	} {
		err := p.Find()
		if err != nil {
			t.Errorf("%s: %v", p.Name, err)
		}
	}
}

// Closing a port doesn't wait for a write the driver blocks: here a pipe
// nobody reads, with a full buffer.
func TestCloseCancelsBlockedWrite(t *testing.T) {
	t.Parallel()

	var r, w syscall.Handle

	err := syscall.CreatePipe(&r, &w, nil, 4096)
	if err != nil {
		t.Fatal(err)
	}

	t.Cleanup(func() { _ = syscall.CloseHandle(r) })

	p := newPort(w)
	done := make(chan error, 1)

	go func() {
		_, err := p.Write(make([]byte, 1<<16))
		done <- err
	}()

	// Wait until the write is in progress, then a little longer, so that
	// it has reached the pipe; Close cancels again anyway if not.
	c, ok := p.(*idleCloser)
	if !ok {
		t.Fatalf("newPort returned a %T", p)
	}

	for writing := false; !writing; {
		select {
		case err := <-done:
			t.Fatalf("the write didn't block: %v", err)
		case <-time.After(10 * time.Millisecond):
		}

		c.mu.Lock()
		writing = c.wrote != nil
		c.mu.Unlock()
	}

	time.Sleep(100 * time.Millisecond)

	err = p.Close()
	if err != nil {
		t.Errorf("close: %v", err)
	}

	select {
	case err := <-done:
		if err == nil {
			t.Error("the cancelled write reported success")
		}
	case <-time.After(5 * time.Second):
		t.Error("Close didn't cancel the blocked write")
	}
}
