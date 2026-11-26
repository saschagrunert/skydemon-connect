// Package testutil holds helpers shared by the tests of several packages.
package testutil

import (
	"bytes"
	"log"
	"net"
	"net/netip"
	"sync"
	"testing"
)

// Buffer is a bytes.Buffer that is safe for concurrent use, e.g. as the log
// output while background goroutines log.
type Buffer struct {
	b  bytes.Buffer
	mu sync.Mutex
}

// Write appends p.
func (b *Buffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()

	return b.b.Write(p)
}

// String returns what was written so far.
func (b *Buffer) String() string {
	b.mu.Lock()
	defer b.mu.Unlock()

	return b.b.String()
}

// Log returns a logger that writes to the test's output, shown when the
// test fails or with -v.
func Log(tb testing.TB) *log.Logger {
	tb.Helper()

	return log.New(tb.Output(), "", 0)
}

// ListenUDP listens on a free loopback UDP port until the test ends.
func ListenUDP(tb testing.TB) *net.UDPConn {
	tb.Helper()

	loopback := net.UDPAddrFromAddrPort(netip.MustParseAddrPort("127.0.0.1:0"))

	conn, err := net.ListenUDP("udp4", loopback)
	if err != nil {
		tb.Fatal(err)
	}

	tb.Cleanup(func() { _ = conn.Close() })

	return conn
}
