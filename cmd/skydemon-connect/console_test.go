package main

import (
	"bytes"
	"context"
	"strings"
	"testing"
	"testing/synctest"
	"time"

	"github.com/saschagrunert/skydemon-connect/internal/simconnect"
	"github.com/saschagrunert/skydemon-connect/internal/testutil"
)

func TestStatusLine(t *testing.T) {
	t.Parallel()

	var out bytes.Buffer

	s := &statusLine{out: &out}
	s.show("status")

	_, err := s.Write([]byte("log\n"))
	if err != nil {
		t.Fatal(err)
	}

	s.end() // nothing shown: no extra newline

	if got := out.String(); !strings.HasSuffix(got, clearLine()+"log\n") ||
		!strings.HasPrefix(got, "\rstatus") {
		t.Errorf("output %q", got)
	}
}

// A status longer than the line is cut to it, so that a shorter one
// written over it leaves nothing behind.
func TestStatusLineCuts(t *testing.T) {
	t.Parallel()

	var out bytes.Buffer

	s := &statusLine{out: &out}
	s.show("Error: " + strings.Repeat("é", 2*statusWidth))

	want := "\rError: " + strings.Repeat("é", statusWidth-len("Error: ")-1) + "…"
	if got := out.String(); got != want {
		t.Errorf("output %q, want %q", got, want)
	}
}

func TestRunConsole(t *testing.T) {
	t.Parallel()

	conn := testutil.ListenUDP(t)

	synctest.Test(t, func(t *testing.T) {
		var out testutil.Buffer

		ctx, cancel := context.WithCancel(t.Context())
		done := make(chan error)
		c := flags(t, "-udp", conn.LocalAddr().String(), "-demo", "-demo-lat", "51",
			"-demo-lon", "12")

		go func() { done <- runConsole(ctx, c, &out, input{sim: simconnect.Run}, statusEvery) }()

		time.Sleep(2 * statusEvery)
		synctest.Wait()
		cancel()

		err := <-done
		if err != nil {
			t.Errorf("runConsole: %v", err)
		}

		if got := out.String(); !strings.Contains(got, "demo: circling") ||
			!strings.Contains(got, " kt · trk ") {
			t.Errorf("output %q", got)
		}
	})
}
