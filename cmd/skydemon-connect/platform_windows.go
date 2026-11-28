//go:build windows

package main

import (
	"context"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"runtime/debug"

	"github.com/saschagrunert/skydemon-connect/internal/simconnect"
	"github.com/saschagrunert/skydemon-connect/internal/tray"
	"github.com/saschagrunert/skydemon-connect/internal/win"
)

// tempDirText is shown when the exe runs from inside an archive.
const tempDirText = "SkyDemonConnect is running from inside the zip or a temporary folder." +
	` Extract the zip to a folder of its own first, e.g. %LOCALAPPDATA%\Programs\SkyDemonConnect.`

// prepare restricts where DLLs are loaded from, before anything, e.g. the
// -help message box, loads one.
func prepare() { win.RestrictDLLSearch() }

// run starts the tray, or a console window with -console. The binary is
// built for the GUI subsystem, so without -console no window appears.
func run(c *config) int {
	// Without the exe's path, "Start with MSFS" is off and the log goes to
	// %LOCALAPPDATA%, not to MSFS's folder.
	exe, err := os.Executable()
	if err != nil {
		exe = ""
	}

	// One instance only: a double-click and exe.xml would otherwise both
	// send. Checked before the log is opened, so as not to touch the
	// running instance's log.
	if win.AlreadyRunning() {
		switch {
		case c.auto:
			// Started by MSFS, so silently: a log of its own says why
			// nothing happened.
			logAlreadyRunning(exe)
		case len(os.Args) > 1:
			win.ShowInfo("SkyDemonConnect is already running; quit it to start it with " +
				"these options. " + tray.IconHint)
		default:
			win.ShowInfo("SkyDemonConnect is already running. " + tray.IconHint)
		}

		return 0
	}

	// Run from inside the zip, the DLL is missing and "Start with MSFS"
	// would write a path that is gone tomorrow.
	if insideArchive(filepath.Dir(exe)) {
		win.ShowError(tempDirText)

		return exitError
	}

	if c.console {
		return runConsoleWindow(c)
	}

	return runTray(c, exe)
}

// logAlreadyRunning logs, to a log of this process's own, that another
// copy runs, which an -auto start doesn't show.
func logAlreadyRunning(exe string) {
	f := openOwnLog(logDirs(exe)...)
	if f == nil {
		return
	}

	newLogger(f).Printf("SkyDemonConnect %s: another copy is running; exiting", version)

	_ = f.Close()
}

// watchMSFS returns a context that is done when MSFS, which started this
// copy with -auto, exits, and whether MSFS is watched.
func watchMSFS(ctx context.Context, l *log.Logger) (context.Context, bool) {
	return win.UntilMSFSExits(ctx, l)
}

// onSessionEnd calls fn at logoff or shutdown, before Windows ends the
// process. Without it, which only a failing window leads to, the app is
// ended as before: the sim's session times out, the COM port is closed by
// Windows.
func onSessionEnd(fn func()) { _ = win.OnSessionEnd(fn) }

// runConsoleWindow runs the console mode in a console window of its own.
// Errors are shown in a message box too: the window closes on exit.
func runConsoleWindow(c *config) int {
	out, err := win.Console()
	if err != nil {
		win.ShowError(err.Error())

		return exitError
	}

	// Standard error isn't valid in a GUI-subsystem process: a panic goes
	// to the console instead. A conhost window closes as the process
	// exits, so the text only stays where the console outlives it, e.g. in
	// Windows Terminal.
	_ = debug.SetCrashOutput(out, debug.CrashOptions{})

	err = runConsoleUntilInterrupt(c, out)
	if err != nil {
		win.ShowError(err.Error())

		return exitError
	}

	return 0
}

// logDirs are where the log goes: next to the exe, as when started via
// exe.xml the working directory is MSFS's, or in a read-only folder to
// %LOCALAPPDATA%.
func logDirs(exe string) []string {
	var dirs []string
	if exe != "" {
		dirs = append(dirs, filepath.Dir(exe))
	}

	if local := os.Getenv("LOCALAPPDATA"); local != "" {
		dirs = append(dirs, filepath.Join(local, "SkyDemonConnect"))
	}

	return dirs
}

func runTray(c *config, exe string) int {
	logFile := openLog(logDirs(exe)...)
	l, logPath := newLogger(logFile), ""

	if logFile != nil {
		defer func() { _ = logFile.Close() }()

		logPath = logFile.Name()

		// A panic's stack goes to the log too: the GUI subsystem has no
		// standard error to print it to.
		_ = debug.SetCrashOutput(logFile, debug.CrashOptions{})
	}

	l.Printf("SkyDemonConnect %s starting (auto=%v demo=%v)", version, c.auto, c.demo)

	ctx, watched := context.Background(), false
	if c.auto {
		ctx, watched = watchMSFS(ctx, l)
	}

	r, err := startFeed(
		ctx,
		c,
		input{
			sim:              simconnect.Run,
			routes:           runRoutes,
			quitOnSessionEnd: sessionEndQuits(c.auto, watched),
		},
		l,
	)
	if err != nil {
		l.Printf("startup: %v", err)
		win.ShowError(err.Error())

		return exitError
	}
	defer r.stop()

	err = tray.Run(&tray.Options{
		Feed:         r.feed,
		Quit:         r.quit,
		AutoArgs:     c.passOn,
		Destinations: r.destinations,
		SessionEnd:   r.stop,
		Version:      version,
		LogPath:      logPath,
		ExePath:      exe,
		Log:          l,
		Auto:         c.auto,
	})
	if err != nil {
		l.Printf("tray: %v", err)
		r.stop() // nothing is sent while the error shows
		win.ShowError(err.Error())

		return exitError
	}

	r.stop() // before "bye": the log then ends with the shutdown
	l.Printf("bye")

	return 0
}

func notify(text string, isErr bool) {
	if isErr {
		win.ShowError(text)
	} else {
		win.ShowInfo(text)
	}
}

// documentsDir is the user's Documents folder, wherever it is.
func documentsDir() (string, error) {
	dir, err := win.Documents()
	if err != nil {
		return "", fmt.Errorf("documents: %w", err)
	}

	return dir, nil
}

// msfsRunning reports whether MSFS runs on this PC.
func msfsRunning() bool { return win.MSFSRunning() }
