//go:build !windows

package main

import (
	"path/filepath"
	"sync"
	"testing"

	"github.com/saschagrunert/skydemon-connect/internal/testutil"
)

// Outside Windows a COM port is a file, so NMEA output can be opened.
func TestOpenOutputsSerialFile(t *testing.T) {
	t.Parallel()

	outs, err := openOutputs(
		flags(t, "-udp", "auto", "-com", filepath.Join(t.TempDir(), "nmea.txt")),
		testutil.Log(t),
	)
	if err != nil {
		t.Fatal(err)
	}
	defer outs.shutdown(&sync.WaitGroup{}, testutil.Log(t))

	if outs.Serial == nil || outs.UDP == nil {
		t.Errorf("serial %v, udp %v", outs.Serial, outs.UDP)
	}
}

// -com alone opens no UDP socket.
func TestOpenOutputsSerialOnly(t *testing.T) {
	t.Parallel()

	outs, err := openOutputs(
		flags(t, "-udp=", "-com", filepath.Join(t.TempDir(), "nmea.txt")),
		testutil.Log(t),
	)
	if err != nil {
		t.Fatal(err)
	}
	defer outs.shutdown(&sync.WaitGroup{}, testutil.Log(t))

	if outs.Serial == nil || outs.UDP != nil {
		t.Errorf("serial %v, udp %v", outs.Serial, outs.UDP)
	}
}
