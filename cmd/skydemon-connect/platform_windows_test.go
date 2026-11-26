//go:build windows

package main

import (
	"errors"
	"os/exec"
	"path/filepath"
	"strings"
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

	isPing := func(path string) bool {
		return strings.EqualFold(filepath.Base(path), "PING.EXE")
	}

	_, err = watchProcess(t.Context(), cmd.Process.Pid,
		func(string) bool { return false }, testutil.Log(t))
	if !errors.Is(err, errNotWatched) {
		t.Errorf("a process not accepted: %v", err)
	}

	ctx, err := watchProcess(t.Context(), cmd.Process.Pid, isPing, testutil.Log(t))
	if err != nil {
		t.Fatal(err)
	}

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
