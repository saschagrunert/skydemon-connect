//go:build !windows

package serial

import (
	"fmt"
	"io"
	"os"
	"time"
)

const fileMode = 0o600

// openPort opens name for appending. Outside Windows this exists for
// testing: name can be a file or a pty. A write to a pty nobody reads is
// cancelled through its deadline.
func openPort(name string, _ uint32) (io.WriteCloser, error) {
	const flags = os.O_WRONLY | os.O_CREATE | os.O_APPEND

	f, err := os.OpenFile(name, flags, fileMode)
	if err != nil {
		return nil, fmt.Errorf("open %s: %w", name, err)
	}

	return &idleCloser{port: f, cancel: func() { _ = f.SetWriteDeadline(time.Now()) }}, nil
}
