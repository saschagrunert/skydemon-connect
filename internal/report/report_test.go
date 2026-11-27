package report_test

import (
	"errors"
	"log"
	"strings"
	"testing"
	"testing/synctest"
	"time"

	"github.com/saschagrunert/skydemon-connect/internal/report"
	"github.com/saschagrunert/skydemon-connect/internal/testutil"
)

var errA, errB = errors.New("a"), errors.New("b")

func TestReport(t *testing.T) {
	t.Parallel()

	var out testutil.Buffer

	r := report.New(log.New(&out, "", 0))

	for _, err := range []error{
		nil,
		errA,
		errA, // the same error: not logged again
		errB,
		nil,
		nil, // still working: not logged again
	} {
		r.Report("udp", err)
	}

	if got, want := out.String(), "udp: a\nudp: b\nudp: working again\n"; got != want {
		t.Errorf("log %q, want %q", got, want)
	}

	r.Report("serial", errA)

	if !strings.HasSuffix(out.String(), "serial: a\n") {
		t.Errorf("keys are separate: %q", out.String())
	}

	r.Forget("serial")
	r.Report("serial", errA)

	if strings.Count(out.String(), "serial: a\n") != 2 {
		t.Errorf("a forgotten error is new again: %q", out.String())
	}
}

// A settled Reporter neither logs nor fails on an activity that flaps
// faster than the settle time, but does on one that keeps failing, and
// recovers only once it kept working.
func TestSettled(t *testing.T) {
	t.Parallel()

	synctest.Test(t, func(t *testing.T) {
		var out testutil.Buffer

		const settle = 2 * time.Second

		r := report.NewSettled(log.New(&out, "", 0), settle)
		step := func(err error) {
			r.Report("udp", err)
			time.Sleep(settle / 4)
		}

		for range 20 { // flapping
			step(errA)
			step(nil)
		}

		if out.String() != "" || r.Failing("udp") {
			t.Fatalf("flapping: failing %v, log %q", r.Failing("udp"), out.String())
		}

		for range 5 { // failing for longer than settle
			step(errA)
		}

		if !r.Failing("udp") || out.String() != "udp: a\n" {
			t.Fatalf("failing: %v, log %q", r.Failing("udp"), out.String())
		}

		step(nil) // one success doesn't end it
		step(errA)

		if !r.Failing("udp") {
			t.Fatal("a single success ended the failure")
		}

		for range 5 {
			step(nil)
		}

		if r.Failing("udp") || out.String() != "udp: a\nudp: working again\n" {
			t.Errorf("recovered: failing %v, log %q", r.Failing("udp"), out.String())
		}
	})
}
