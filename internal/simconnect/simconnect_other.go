//go:build !windows

package simconnect

import (
	"context"
	"errors"
	"log"
)

// errUnsupported is reported on platforms without SimConnect.
var errUnsupported = errors.New("SimConnect is only available on Windows; use -demo")

// Run reports through h.Failed that SimConnect only exists on Windows, and
// waits until ctx is done, as the Windows Run does.
func Run(ctx context.Context, l *log.Logger, h Handlers) {
	l.Printf("simconnect: %v", errUnsupported)
	h.Failed(errUnsupported)
	<-ctx.Done()
}
