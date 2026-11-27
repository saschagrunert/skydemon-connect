//go:build windows

package win

import (
	"context"
	"errors"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/saschagrunert/skydemon-connect/internal/testutil"
)

func TestWatchProcess(t *testing.T) {
	t.Parallel()

	// A child that waits until it is killed.
	cmd := exec.CommandContext(t.Context(), "ping", "-n", "60", "127.0.0.1")

	err := cmd.Start()
	if err != nil {
		t.Fatal(err)
	}

	isPing := func(_ syscall.Handle, path string) bool {
		return strings.EqualFold(filepath.Base(path), "PING.EXE")
	}

	_, err = watchProcess(t.Context(), cmd.Process.Pid,
		func(syscall.Handle, string) bool { return false }, testutil.Log(t))
	if !errors.Is(err, errNotWatched) {
		t.Errorf("a process not accepted: %v", err)
	}

	ctx, err := watchProcess(t.Context(), cmd.Process.Pid, isPing, testutil.Log(t))
	if err != nil {
		t.Fatal(err)
	}

	// The child started after us, so it isn't taken for our parent.
	const queryLimitedInformation = 0x1000 // PROCESS_QUERY_LIMITED_INFORMATION

	h, err := syscall.OpenProcess(queryLimitedInformation, false, uint32(cmd.Process.Pid))
	if err != nil {
		t.Fatal(err)
	}

	if startedBeforeUs(h) {
		t.Error("a child counts as started before us")
	}

	_ = syscall.CloseHandle(h)

	select {
	case <-ctx.Done():
		t.Fatal("done before the process exited")
	case <-time.After(100 * time.Millisecond):
	}

	_ = cmd.Process.Kill()
	_ = cmd.Wait()

	select {
	case <-ctx.Done():
	case <-time.After(10 * time.Second):
		t.Error("not done after the process exited")
	}
}

// pollExit, the fallback when waiting fails, reports a process that
// exited, and gives up once ctx is done for one that still runs.
func TestPollExit(t *testing.T) {
	t.Parallel()

	cmd := exec.CommandContext(t.Context(), "cmd", "/c", "exit 3")

	err := cmd.Start()
	if err != nil {
		t.Fatal(err)
	}

	const access = 0x00100000 | 0x1000 // SYNCHRONIZE, PROCESS_QUERY_LIMITED_INFORMATION

	h, err := syscall.OpenProcess(access, false, uint32(cmd.Process.Pid))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = syscall.CloseHandle(h) }()

	_ = cmd.Wait()

	if !pollExit(t.Context(), h) {
		t.Error("an exited process: not reported")
	}

	self, err := syscall.GetCurrentProcess()
	if err != nil {
		t.Fatal(err)
	}

	ctx, cancel := context.WithCancel(t.Context())
	cancel()

	if pollExit(ctx, self) {
		t.Error("this process: reported as exited")
	}
}
