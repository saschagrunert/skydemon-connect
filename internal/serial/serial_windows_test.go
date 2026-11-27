//go:build windows

package serial_test

import (
	"testing"

	"github.com/saschagrunert/skydemon-connect/internal/serial"
)

// A port that isn't there opens fine, for a USB adapter that comes later;
// writes fail until it is there.
func TestOpenMissingPort(t *testing.T) {
	t.Parallel()

	for _, name := range []string{"COM250", `\\.\COM251`} {
		port := serial.Open(name, 9600)

		_, err := port.Write([]byte("$A\r\n"))
		if err == nil {
			t.Errorf("%s: wrote to a COM port that should not exist", name)
		}

		_ = port.Close()
	}
}
