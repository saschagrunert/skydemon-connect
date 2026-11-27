//go:build !windows

package serial_test

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/saschagrunert/skydemon-connect/internal/serial"
)

func TestOpenFile(t *testing.T) {
	t.Parallel()

	path := filepath.Join(t.TempDir(), "nmea.txt")

	for _, line := range []string{"$A\r\n", "$B\r\n"} {
		port := serial.Open(path, 115200)

		_, err := port.Write([]byte(line))
		if err != nil {
			t.Fatal(err)
		}

		err = port.Close()
		if err != nil {
			t.Fatal(err)
		}
	}

	b, err := os.ReadFile(path)
	if err != nil || string(b) != "$A\r\n$B\r\n" {
		t.Errorf("got %q %v, want both lines appended", b, err)
	}

	// A port that isn't there opens fine; writes fail until it is.
	missing := serial.Open(filepath.Join(t.TempDir(), "missing", "x"), 115200)

	_, err = missing.Write([]byte("$A\r\n"))
	if err == nil {
		t.Error("write to a missing port succeeded")
	}

	_ = missing.Close()
}
