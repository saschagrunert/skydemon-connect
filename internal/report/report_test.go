package report_test

import (
	"errors"
	"log"
	"strings"
	"testing"

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
