//go:build windows

package win

import (
	"fmt"
	"os"
	"syscall"
	"testing"
)

func TestAlreadyRunning(t *testing.T) {
	t.Parallel()

	name := fmt.Sprintf(`Local\SkyDemonConnectTest%d`, os.Getpid())

	if alreadyRunning(name) {
		t.Fatal("first call: already running")
	}

	if !alreadyRunning(name) {
		t.Error("second call: not running")
	}
}

// Every Win32 procedure exists: NewProc only looks a name up on the first
// call, which a typo would turn into a panic on a user's PC.
func TestProcsExist(t *testing.T) {
	t.Parallel()

	for _, p := range []*syscall.LazyProc{
		procMessageBoxW, procCreateMutexW, procAllocConsole,
		procSetDefaultDllDirectories, procQueryFullProcessImageNameW,
	} {
		err := p.Find()
		if err != nil {
			t.Error(err)
		}
	}
}
