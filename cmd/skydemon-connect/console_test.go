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

func TestRunConsole(t *testing.T) {
	t.Parallel()

	conn := testutil.ListenUDP(t)

	synctest.Test(t, func(t *testing.T) {
		var out testutil.Buffer

		ctx, cancel := context.WithCancel(t.Context())
		done := make(chan error)
		c := flags(t, "-udp", conn.LocalAddr().String(), "-demo", "-demo-lat", "51",
			"-demo-lon", "12")

		go func() { done <- runConsole(ctx, c, &out, simconnect.Run, statusEvery) }()

		time.Sleep(2 * statusEvery)
		synctest.Wait()
		cancel()

		err := <-done
		if err != nil {
			t.Errorf("runConsole: %v", err)
		}

		if got := out.String(); !strings.Contains(got, "demo: circling") ||
			!strings.Contains(got, "\r30 Hz · ") {
			t.Errorf("output %q", got)
		}
	})
}
