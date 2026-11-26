//go:build !windows

package simconnect

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/saschagrunert/skydemon-connect/internal/position"
	"github.com/saschagrunert/skydemon-connect/internal/testutil"
)

// Outside Windows, Run reports the missing SimConnect and waits, like the
// Windows Run, until ctx is done.
func TestRunUnsupported(t *testing.T) {
	t.Parallel()

	var failed []error

	ctx, cancel := context.WithTimeout(t.Context(), 10*time.Second)
	defer cancel()

	h := Handlers{
		Update: func(position.Sample) { t.Error("an update") },
		Live:   func(bool) { t.Error("live") },
		Ended:  func() { t.Error("ended") },
		Failed: func(err error) {
			failed = append(failed, err)

			cancel()
		},
	}

	Run(ctx, testutil.Log(t), h)

	if len(failed) != 1 || !errors.Is(failed[0], errUnsupported) {
		t.Errorf("failed %v", failed)
	}
}
