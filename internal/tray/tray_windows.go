//go:build windows

package tray

import (
	"errors"
	"fmt"
	"log"
	"path/filepath"
	"runtime"
	"syscall"
	"unsafe"

	"github.com/saschagrunert/skydemon-connect/internal/exexml"
	"github.com/saschagrunert/skydemon-connect/internal/feed"
	"github.com/saschagrunert/skydemon-connect/internal/icon"
)

const (
	timerID = 1
	tickMS  = 1000
	iconID  = 1
	// shellExecuteOK: ShellExecuteW returns a value above 32 on success.
	shellExecuteOK = 32
	// smallIconSize is the tray icon size at 100 % scaling.
	smallIconSize = 16
)

var errIconNotShown = errors.New("Shell_NotifyIcon failed")

// Options configure the tray.
type Options struct {
	Feed *feed.Feed
	// Quit ends the tray when closed: with -auto when MSFS closes or the
	// process that started us exits. Nil never quits.
	Quit <-chan struct{}
	// Version is shown in the menu.
	Version string
	// LogPath is opened by "Open log".
	LogPath string
	// ExePath is written to exe.xml by "Start with MSFS".
	ExePath string
	// MSFS are the exe.xml files "Start with MSFS" edits; nil finds
	// them each time, so that an MSFS installed meanwhile is seen.
	MSFS exexml.Installs
	// AutoArgs are the options "Start with MSFS" writes after -auto, as
	// given, e.g. "-name=My Sim"; they are quoted as needed.
	AutoArgs []string
	// Destinations, if set, returns where positions go, one menu line
	// each, e.g. "UDP: 127.0.0.1, 192.168.1.255" (the default port left
	// out) or "NMEA: COM11 at 115200 baud".
	Destinations func() []string
	// SessionEnd, if set, is called when Windows ends the session (logoff,
	// shutdown): the process may be ended as soon as the tray handled
	// that, so stop the work there; Run returns too, if there is time.
	SessionEnd func()
	// Log receives what the tray does.
	Log *log.Logger
	// Auto: started by MSFS; no welcome balloon.
	Auto bool
}

// className is the tray window's class.
const className = "SkyDemonConnectTray"

type tray struct {
	opts Options
	// showError shows an error to the user: ShowError, or a test's own.
	showError      func(string)
	icons          map[feed.Kind]uintptr
	hwnd           uintptr
	taskbarCreated uintptr
	nid            notifyIconData
	status         tracker
	// added: the icon is believed to be in the notification area. A
	// failed call, e.g. at logon while Explorer is busy, may or may not
	// have changed that, so showIcon tries both ways.
	added bool
	// iconFailed: that failure was logged; logged again once it worked.
	iconFailed bool
}

// installs returns the exe.xml files to edit.
func (t *tray) installs() exexml.Installs {
	if t.opts.MSFS != nil {
		return t.opts.MSFS
	}

	return exexml.Find()
}

// Run shows the tray icon and runs the message loop until the user quits or
// opts.Quit is closed. It runs once per process.
func Run(opts *Options) error {
	// The window and its message loop must stay on one OS thread.
	runtime.LockOSThread()

	defer runtime.UnlockOSThread()

	// ShellExecute, for Open log, may use shell extensions that need COM
	// on this thread. The exe's manifest makes it DPI aware.
	_, _, _ = procCoInitializeEx.Call(0, coinitShell)

	t := &tray{opts: *opts, icons: map[feed.Kind]uintptr{}, showError: ShowError}

	err := t.create()
	if err != nil {
		return err
	}

	// Whichever way the loop ends, no icon is left behind.
	defer t.removeIcon()

	// No greeting when MSFS started the app, nor before a problem balloon.
	if !opts.Auto && t.status.last != feed.Failed {
		t.balloon(welcomeTitle, welcomeText, niifInfo)
	}

	return t.loop()
}

// iconSize is the small icon size for the window's monitor scaling.
func (t *tray) iconSize() int {
	if procGetDpiForWindow.Find() == nil && procGetSystemMetricsForDpi.Find() == nil {
		dpi, _, _ := procGetDpiForWindow.Call(t.hwnd)
		if dpi != 0 {
			size, _, _ := procGetSystemMetricsForDpi.Call(smCxSmIcon, dpi)
			if size != 0 {
				return int(size)
			}
		}
	}

	size, _, _ := procGetSystemMetrics.Call(smCxSmIcon)
	if size == 0 {
		return smallIconSize
	}

	return int(size)
}

// makeIcons draws the icons for the current scaling, replacing old ones.
func (t *tray) makeIcons() error {
	size := t.iconSize()

	for _, kind := range kinds() {
		h, err := makeIcon(icon.Render(size, colour(kind)))
		if err != nil {
			return err
		}

		if old := t.icons[kind]; old != 0 {
			_, _, _ = procDestroyIcon.Call(old)
		}

		t.icons[kind] = h
	}

	return nil
}

func (t *tray) create() error {
	inst, _, _ := procGetModuleHandleW.Call(0)
	cls, title := utf16Ptr(className), utf16Ptr("SkyDemonConnect")

	wc := wndClassEx{WndProc: syscall.NewCallback(t.wndProc), Instance: inst, ClassName: cls}
	wc.Size = uint32(unsafe.Sizeof(wc))

	if r, _, err := procRegisterClassExW.Call(uintptr(unsafe.Pointer(&wc))); r == 0 {
		return fmt.Errorf("RegisterClassEx: %w", err)
	}

	hwnd, _, err := procCreateWindowExW.Call(
		0,
		uintptr(unsafe.Pointer(cls)),
		uintptr(unsafe.Pointer(title)),
		0,
		0,
		0,
		0,
		0,
		0,
		0,
		inst,
		0,
	)
	if hwnd == 0 {
		return fmt.Errorf("CreateWindowEx: %w", err)
	}

	t.hwnd = hwnd
	t.allowShellMessages()

	err = t.makeIcons()
	if err != nil {
		return err
	}

	// The timer first: without it, an icon added now would stay forever.
	if r, _, err := procSetTimer.Call(hwnd, timerID, tickMS, 0); r == 0 {
		return fmt.Errorf("SetTimer: %w", err)
	}

	t.nid.Size = uint32(unsafe.Sizeof(t.nid))
	t.nid.Wnd = hwnd
	t.nid.ID = iconID
	t.nid.CallbackMessage = wmTray
	t.refresh()

	return nil
}

// allowShellMessages registers TaskbarCreated (Explorer restarted) and
// lets Explorer's messages through: when the app runs elevated (started by
// an elevated MSFS), UIPI drops them, as Explorer runs unelevated.
func (t *tray) allowShellMessages() {
	name := utf16Ptr("TaskbarCreated")
	t.taskbarCreated, _, _ = procRegisterWindowMessageW.Call(uintptr(unsafe.Pointer(name)))

	for _, m := range []uintptr{t.taskbarCreated, wmTray} {
		if m != 0 {
			_, _, _ = procChangeWindowMsgFilter.Call(t.hwnd, m, msgfltAllow, 0)
		}
	}
}

// shell calls Shell_NotifyIcon and reports whether it worked.
func (t *tray) shell(op uintptr, flags uint32) bool {
	t.nid.Flags = flags
	r, _, _ := procShellNotifyIconW.Call(op, uintptr(unsafe.Pointer(&t.nid)))

	return r != 0
}

// showIcon updates the icon, or adds it when it isn't there. Whether it is
// isn't always known: a call that timed out, e.g. with Explorer busy at
// logon, may have worked. So the other operation is tried too, an add
// when an update fails and the other way round, and either working means
// the icon is there. A failure of both is logged once, and the next
// refresh tries again: without the icon, the app couldn't be quit.
func (t *tray) showIcon() {
	const flags = nifMessage | nifIcon | nifTip

	ops := []uintptr{nimModify, nimAdd}
	if !t.added {
		ops = []uintptr{nimAdd, nimModify}
	}

	t.added = t.shell(ops[0], flags) || t.shell(ops[1], flags)

	switch {
	case !t.added && !t.iconFailed:
		t.iconFailed = true
		t.opts.Log.Printf("tray: %v, trying again", errIconNotShown)
	case t.added && t.iconFailed:
		t.iconFailed = false
		t.opts.Log.Printf("tray: icon shown")
	}
}

// removeIcon takes the icon out of the notification area, also when an
// earlier failure left it unclear whether it is there.
func (t *tray) removeIcon() {
	t.shell(nimDelete, 0)
	t.added = false
}

// refresh updates icon and tooltip from the feed status.
func (t *tray) refresh() {
	st := t.opts.Feed.Status()
	t.nid.Icon = t.icons[st.Kind]
	copyUTF16(t.nid.Tip[:], feed.TooltipTitle+st.String())
	t.showIcon()

	if !t.status.update(st.Kind, st.Err) {
		return
	}

	t.opts.Log.Printf("tray: status: %s", st.String())

	if st.Kind == feed.Failed {
		t.balloon(problemTitle, st.Err, niifError)
	}
}

func (t *tray) balloon(title, text string, flags uint32) {
	t.nid.InfoFlags = flags

	copyUTF16(t.nid.InfoTitle[:], title)
	copyUTF16(t.nid.Info[:], text)
	t.shell(nimModify, nifInfo)
}

// session handles Windows ending the session (logoff, shutdown): the
// session may end, and when it does, the app stops.
func (t *tray) session(message uint32, ending uintptr) uintptr {
	if message == wmQueryEndSession {
		return 1 // it may end
	}

	if ending != 0 {
		t.endSession()
	}

	return 0
}

// endSession stops the app as Windows ends the session; it may end the
// process once the window procedure returns.
func (t *tray) endSession() {
	t.opts.Log.Printf("tray: Windows is ending the session")
	t.removeIcon()

	if t.opts.SessionEnd != nil {
		t.opts.SessionEnd()
	}

	_, _, _ = procPostQuitMessage.Call(0)
}

func (t *tray) quit() {
	t.removeIcon()

	_, _, _ = procPostQuitMessage.Call(0)
}

// loop runs the message loop until WM_QUIT, or GetMessage fails.
func (t *tray) loop() error {
	var message msg

	for {
		r, _, err := procGetMessageW.Call(uintptr(unsafe.Pointer(&message)), 0, 0, 0)

		switch int32(r) { //nolint:gosec // BOOL, -1 on error
		case 0:
			return nil
		case -1:
			return fmt.Errorf("GetMessage: %w", err)
		}

		_, _, _ = procTranslateMessage.Call(uintptr(unsafe.Pointer(&message)))
		_, _, _ = procDispatchMessageW.Call(uintptr(unsafe.Pointer(&message)))
	}
}

func appendMenu(handle uintptr, item menuItem) {
	if item.separator {
		_, _, _ = procAppendMenuW.Call(handle, mfSeparator, 0, 0)

		return
	}

	flags := uintptr(mfString)

	switch {
	case item.id == 0:
		flags |= mfGrayed
	case item.checked:
		flags |= mfChecked
	}

	p := utf16Ptr(item.text)
	_, _, _ = procAppendMenuW.Call(handle, flags, uintptr(item.id), uintptr(unsafe.Pointer(p)))
}

func (t *tray) showMenu() {
	handle, _, _ := procCreatePopupMenu.Call()
	defer func() { _, _, _ = procDestroyMenu.Call(handle) }()

	state := &menuState{
		version:  t.opts.Version,
		status:   t.opts.Feed.Status().String(),
		canStart: filepath.IsAbs(t.opts.ExePath),
	}

	if t.opts.Destinations != nil {
		state.destinations = t.opts.Destinations()
	}

	if state.canStart {
		state.msfs = t.installs().State(t.opts.ExePath, t.opts.AutoArgs)
	}

	for _, item := range menu(state) {
		appendMenu(handle, item)
	}

	var pt point

	_, _, _ = procGetCursorPos.Call(uintptr(unsafe.Pointer(&pt)))
	// Without this the menu doesn't close when clicking elsewhere.
	_, _, _ = procSetForegroundWindow.Call(t.hwnd)

	x, y := uintptr(pt.X), uintptr(pt.Y) //nolint:gosec // screen coordinates, an int in C

	cmd, _, _ := procTrackPopupMenu.Call(
		handle,
		tpmReturnCmd|tpmNoNotify|tpmRightButton,
		x,
		y,
		0,
		t.hwnd,
		0,
	)
	_, _, _ = procPostMessageW.Call(t.hwnd, wmNull, 0, 0)

	t.command(int(cmd))
}

func (t *tray) command(cmd int) {
	switch cmd {
	case idMSFS:
		t.toggleMSFS()
	case idLog:
		t.openLog()
	case idQuit:
		t.quit()
	}
}

// openLog opens the log in the program registered for .log files.
func (t *tray) openLog() {
	if t.opts.LogPath == "" {
		t.showError(noLogText)

		return
	}

	verb, path := utf16Ptr("open"), utf16Ptr(t.opts.LogPath)

	r, _, err := procShellExecuteW.Call(0, uintptr(unsafe.Pointer(verb)),
		uintptr(unsafe.Pointer(path)), 0, 0, swShowNormal)
	if r <= shellExecuteOK {
		t.opts.Log.Printf("tray: open log %s: %d, %v", t.opts.LogPath, r, err)
		t.showError("Could not open the log " + t.opts.LogPath + ".")
	}
}

func (t *tray) toggleMSFS() {
	// A click removes this copy's own entry, and replaces any other, e.g.
	// for a moved exe, other options or one disabled by hand, with this
	// copy's.
	installs := t.installs()
	enable := installs.State(t.opts.ExePath, t.opts.AutoArgs) != exexml.Ours

	err := installs.Set(enable, t.opts.ExePath, t.opts.AutoArgs)
	if err != nil {
		t.opts.Log.Printf("tray: exe.xml: %v", err)
		t.showError("Could not update MSFS exe.xml:\n" + err.Error())

		return
	}

	t.opts.Log.Printf("tray: start with MSFS: %v (%s %v)", enable, t.opts.ExePath, t.opts.AutoArgs)

	if enable {
		t.balloon(addedTitle, addedMessage(t.opts.AutoArgs), niifInfo)
	} else {
		t.balloon(removedTitle, removedText, niifInfo)
	}
}

// explorerRestarted adds the icon again, drawn anew: Explorer restarted,
// maybe after a scaling change.
func (t *tray) explorerRestarted() {
	err := t.makeIcons()
	if err != nil {
		t.opts.Log.Printf("tray: icons: %v", err)
	}

	t.added = false
	t.refresh()
}

// wndProc is the window procedure. Run creates the window once per
// process, so the callback, of which Windows allows a limited number, is
// made once too.
//
// message is a UINT: as a uint32, the undefined upper half of its 64-bit
// register is ignored.
func (t *tray) wndProc(hwnd uintptr, message uint32, wparam, lparam uintptr) uintptr {
	switch {
	case message == wmTray:
		switch lparam & loWordMask {
		case wmLButtonUp, wmRButtonUp, wmContextMenu:
			t.showMenu()
		}

		return 0
	case message == wmTimer:
		select {
		case <-t.opts.Quit:
			t.quit()
		default:
			t.refresh()
		}

		return 0
	case message == wmClose:
		t.quit()

		return 0
	case message == wmQueryEndSession, message == wmEndSession:
		return t.session(message, wparam)
	case t.taskbarCreated != 0 && uintptr(message) == t.taskbarCreated:
		t.explorerRestarted()

		return 0
	}

	r, _, _ := procDefWindowProcW.Call(hwnd, uintptr(message), wparam, lparam)

	return r
}
