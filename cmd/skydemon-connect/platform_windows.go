//go:build windows

package main

import (
	"context"
	"errors"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"runtime/debug"
	"strings"
	"syscall"
	"unsafe"

	"github.com/saschagrunert/skydemon-connect/internal/simconnect"
	"github.com/saschagrunert/skydemon-connect/internal/tray"
)

// tempDirText is shown when the exe runs from inside an archive.
const tempDirText = "SkyDemonConnect is running from inside the zip or a temporary folder." +
	` Extract the zip to a folder of its own first, e.g. C:\Tools\SkyDemonConnect.`

var errNotWatched = errors.New("not the process to watch")

// run starts the tray, or a console window with -console. The binary is
// built for the GUI subsystem, so without -console no window appears.
func run(c *config) int {
	// One instance only: a double-click and exe.xml would otherwise both
	// send. Checked before the log is opened, so as not to touch the
	// running instance's log.
	if tray.AlreadyRunning() {
		switch {
		case c.auto:
		case len(os.Args) > 1:
			tray.ShowInfo("SkyDemonConnect is already running; quit it to start it with " +
				"these options. " + tray.IconHint)
		default:
			tray.ShowInfo("SkyDemonConnect is already running. " + tray.IconHint)
		}

		return 0
	}

	exe, err := os.Executable()
	if err != nil {
		exe = "SkyDemonConnect.exe"
	}

	// Run from inside the zip, the DLL is missing and "Start with MSFS"
	// would write a path that is gone tomorrow.
	if insideArchive(filepath.Dir(exe)) {
		tray.ShowError(tempDirText)

		return exitError
	}

	if c.console {
		return runConsoleWindow(c)
	}

	return runTray(c, exe)
}

// runConsoleWindow runs the console mode in a console window of its own.
// Errors are shown in a message box too: the window closes on exit.
func runConsoleWindow(c *config) int {
	out, err := tray.Console()
	if err != nil {
		tray.ShowError(err.Error())

		return exitError
	}

	err = runConsoleUntilInterrupt(c, out)
	if err != nil {
		tray.ShowError(err.Error())

		return exitError
	}

	return 0
}

func runTray(c *config, exe string) int {
	// The log lives next to the exe, as when started via exe.xml the
	// working directory is MSFS's; in a read-only folder it moves to
	// %LOCALAPPDATA%.
	dirs := []string{filepath.Dir(exe)}
	if local := os.Getenv("LOCALAPPDATA"); local != "" {
		dirs = append(dirs, filepath.Join(local, "SkyDemonConnect"))
	}

	l, logFile, logPath := openLog(dirs...)
	if logFile != nil {
		defer func() { _ = logFile.Close() }()

		// A panic's stack goes to the log too: the GUI subsystem has no
		// standard error to print it to.
		_ = debug.SetCrashOutput(logFile, debug.CrashOptions{})
	}

	l.Printf("SkyDemonConnect %s starting (auto=%v demo=%v)", version, c.auto, c.demo)

	ctx, watched := context.Background(), false
	if c.auto {
		ctx, watched = untilMSFSExits(ctx, l)
	}

	r, err := startFeed(
		ctx,
		c,
		input{sim: simconnect.Run, endedQuits: endedQuits(c.auto, watched)},
		l,
	)
	if err != nil {
		l.Printf("startup: %v", err)
		tray.ShowError(err.Error())

		return exitError
	}
	defer r.stop()

	err = tray.Run(&tray.Options{
		Feed:       r.feed,
		Quit:       r.quit,
		MSFS:       nil, // found when needed
		AutoArgs:   c.passOn,
		SessionEnd: r.stop,
		Version:    version,
		LogPath:    logPath,
		ExePath:    exe,
		Log:        l,
		Auto:       c.auto,
	})
	if err != nil {
		l.Printf("tray: %v", err)
		tray.ShowError(err.Error())

		return exitError
	}

	l.Printf("bye")

	return 0
}

// untilMSFSExits returns a context that is done when the parent process,
// MSFS with -auto, exits. That ends the app also when MSFS closes before
// SimConnect ever answered. A parent that isn't MSFS, e.g. a launcher that
// exits right away, is not watched; the bool reports whether it is.
func untilMSFSExits(ctx context.Context, l *log.Logger) (context.Context, bool) {
	isMSFS := func(path string) bool {
		return strings.HasPrefix(strings.ToLower(filepath.Base(path)), "flightsimulator")
	}

	ctx, err := watchProcess(ctx, os.Getppid(), isMSFS, l)
	if err != nil {
		l.Printf("auto: not watching the process that started us: %v", err)

		return ctx, false
	}

	return ctx, true
}

// watchProcess returns a context that is done when process pid exits, if
// accept accepts its executable's path; otherwise ctx and an error. The
// watching goroutine lives as long as that process, which with -auto is as
// long as the app.
func watchProcess(
	ctx context.Context,
	pid int,
	accept func(path string) bool,
	l *log.Logger,
) (context.Context, error) {
	const access = 0x00100000 | 0x1000 // SYNCHRONIZE, PROCESS_QUERY_LIMITED_INFORMATION

	h, err := syscall.OpenProcess(access, false, uint32(pid)) //nolint:gosec // a PID
	if err != nil {
		return ctx, fmt.Errorf("open process %d: %w", pid, err)
	}

	name := processName(h)
	if !accept(name) {
		_ = syscall.CloseHandle(h)

		return ctx, fmt.Errorf("%w: %q", errNotWatched, name)
	}

	ctx, cancel := context.WithCancel(ctx)

	go func() {
		defer func() { _ = syscall.CloseHandle(h) }()

		_, _ = syscall.WaitForSingleObject(h, syscall.INFINITE)

		l.Printf("auto: %s exited; exiting", filepath.Base(name))
		cancel()
	}()

	return ctx, nil
}

var procQueryFullProcessImageNameW = syscall.NewLazyDLL("kernel32.dll").
	NewProc("QueryFullProcessImageNameW")

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

func notify(text string, isErr bool) {
	if isErr {
		tray.ShowError(text)
	} else {
		tray.ShowInfo(text)
	}
}
