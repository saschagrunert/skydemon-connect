//go:build windows

package win

import (
	"context"
	"errors"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"syscall"
	"time"
	"unsafe"
)

var errNotWatched = errors.New("not the process to watch")

// UntilMSFSExits returns a context that is done when the parent process,
// MSFS with -auto, exits. That ends the app also when MSFS closes before
// SimConnect ever answered. A parent that isn't MSFS, e.g. a launcher that
// exits right away, is not watched; the bool reports whether it is.
func UntilMSFSExits(ctx context.Context, l *log.Logger) (context.Context, bool) {
	accept := func(h syscall.Handle, path string) bool {
		return isMSFS(path) && startedBeforeUs(h)
	}

	ctx, err := watchProcess(ctx, os.Getppid(), accept, l)
	if err != nil {
		l.Printf("auto: not watching the process that started us: %v", err)

		return ctx, false
	}

	return ctx, true
}

// watchProcess returns a context that is done when process pid exits, if
// accept accepts it and its executable's path; otherwise ctx and an
// error. The watching goroutine lives as long as that process, which with
// -auto is as long as the app.
func watchProcess(
	ctx context.Context,
	pid int,
	accept func(h syscall.Handle, path string) bool,
	l *log.Logger,
) (context.Context, error) {
	const access = 0x00100000 | 0x1000 // SYNCHRONIZE, PROCESS_QUERY_LIMITED_INFORMATION

	h, err := syscall.OpenProcess(access, false, uint32(pid)) //nolint:gosec // a PID
	if err != nil {
		return ctx, fmt.Errorf("open process %d: %w", pid, err)
	}

	name := processName(h)
	if !accept(h, name) {
		_ = syscall.CloseHandle(h)

		return ctx, fmt.Errorf("%w: %q", errNotWatched, name)
	}

	ctx, cancel := context.WithCancel(ctx)

	go func() {
		defer func() { _ = syscall.CloseHandle(h) }()

		ev, err := syscall.WaitForSingleObject(h, syscall.INFINITE)
		if ev != syscall.WAIT_OBJECT_0 {
			// Not an exit. Without it, nothing would end the app with
			// MSFS: poll the exit code instead.
			l.Printf("auto: waiting for %s failed (%v), polling it", filepath.Base(name), err)

			if !pollExit(ctx, h) {
				if ctx.Err() == nil {
					l.Printf("auto: can't check %s any more; it won't end the app",
						filepath.Base(name))
				}

				return
			}
		}

		l.Printf("auto: %s exited; exiting", filepath.Base(name))
		cancel()
	}()

	return ctx, nil
}

// pollExit returns once process h exited, true, or ctx is done or the
// exit code can't be read, false.
func pollExit(ctx context.Context, h syscall.Handle) bool {
	const (
		stillActive = 259 // STILL_ACTIVE
		every       = 5 * time.Second
	)

	tick := time.NewTicker(every)
	defer tick.Stop()

	for {
		var code uint32

		err := syscall.GetExitCodeProcess(h, &code)
		if err != nil {
			return false
		}

		if code != stillActive {
			return true
		}

		select {
		case <-ctx.Done():
			return false
		case <-tick.C:
		}
	}
}

var procQueryFullProcessImageNameW = kernel32.NewProc("QueryFullProcessImageNameW")

// startedBeforeUs reports whether process h started before this one, as
// the parent that started us did. A parent PID can be reused by an
// unrelated process once the parent exited; that one started later.
func startedBeforeUs(h syscall.Handle) bool {
	var theirs, ours, unused syscall.Filetime

	self, err := syscall.GetCurrentProcess()
	if err != nil {
		return false
	}

	if syscall.GetProcessTimes(h, &theirs, &unused, &unused, &unused) != nil ||
		syscall.GetProcessTimes(self, &ours, &unused, &unused, &unused) != nil {
		return false
	}

	return theirs.Nanoseconds() <= ours.Nanoseconds()
}

// processName returns the path of a process's executable, or "".
func processName(h syscall.Handle) string {
	const size = syscall.MAX_LONG_PATH

	buf := make([]uint16, size)
	n := uint32(size)

	r, _, _ := procQueryFullProcessImageNameW.Call(
		uintptr(h), 0, uintptr(unsafe.Pointer(&buf[0])), uintptr(unsafe.Pointer(&n)))
	if r == 0 {
		return ""
	}

	return syscall.UTF16ToString(buf[:n])
}
