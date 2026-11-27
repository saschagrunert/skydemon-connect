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
	"github.com/saschagrunert/skydemon-connect/internal/win"
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
	// msfs are the exe.xml files "Start with MSFS" edits, set by tests;
	// nil finds them each time, so that an MSFS installed meanwhile is
	// seen.
	msfs exexml.Installs
}

// className is the tray window's class.
const className = "SkyDemonConnectTray"

type tray struct {
	opts Options
	// showError shows an error to the user in a box the tray window owns,
	// or a test's own.
	showError      func(string)
	icons          map[feed.Kind]uintptr
	hwnd           uintptr
	taskbarCreated uintptr
	nid            notifyIconData
	status         tracker
	// iconSize is the size the icons were drawn at.
	iconSize int
	// shownIcon and shownTip are what the notification area was last
	// given, so that an unchanged state isn't sent again every second.
	shownIcon uintptr
	shownTip  string
	// added: the icon is believed to be in the notification area. A
	// failed call, e.g. at logon while Explorer is busy, may or may not
	// have changed that, so showIcon tries both ways.
	added bool
	// iconFailed: that failure was logged; logged again once it worked.
	iconFailed bool
	// pending is the latest balloon that couldn't be shown without the
	// icon, e.g. that SimConnect.dll is missing; shown once it is added.
	pending *balloonText
	// modal: a message box is open; tray clicks are ignored until it is
	// closed, so that the menu can't run a second command meanwhile.
	modal bool
	// ending: Windows is ending the session; the icon is left to it,
	// since a call into Explorer, which is ending too, may stall.
	ending bool
	// coInit: COM was initialised on the tray thread, to be undone.
	coInit bool
	// timer: the refresh timer is set, to be killed.
	timer bool
}

// installs returns the exe.xml files to edit.
func (t *tray) installs() exexml.Installs {
	if t.opts.msfs != nil {
		return t.opts.msfs
	}

	return exexml.Find()
}

// Run shows the tray icon and runs the message loop until the user quits or
// opts.Quit is closed. It runs once per process.
func Run(opts *Options) error {
	// The window and its message loop must stay on one OS thread.
	runtime.LockOSThread()

	defer runtime.UnlockOSThread()

	t := &tray{opts: *opts, icons: map[feed.Kind]uintptr{}}
	t.showError = t.ownedError

	// ShellExecute, for Open log, may use shell extensions that need COM
	// on this thread. The exe's manifest makes it DPI aware. S_OK and
	// S_FALSE both need a CoUninitialize; a failure doesn't.
	r, _, _ := procCoInitializeEx.Call(0, coinitShell)
	t.coInit = int32(r) >= 0 //nolint:gosec // HRESULT, negative on failure

	// Whichever way Run ends, nothing is left behind: no icon, and no
	// window or timer that a later message box's loop would still serve.
	defer t.destroy()

	err := t.create()
	if err != nil {
		return err
	}

	// No greeting when MSFS started the app, nor before a problem balloon.
	if !opts.Auto && t.status.last != feed.Failed {
		t.balloon(welcomeTitle, welcomeText, niifInfo)
	}

	return t.loop()
}

// smallIconSize is the small icon size for the window's monitor scaling.
func (t *tray) smallIconSize() int {
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
	size := t.smallIconSize()

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

	t.iconSize = size

	return nil
}

// destroyIcons frees the icons.
func (t *tray) destroyIcons() {
	for kind, h := range t.icons {
		_, _, _ = procDestroyIcon.Call(h)

		delete(t.icons, kind)
	}
}

// destroy undoes Run's setup: the icon, unless Windows is ending the
// session, the timer, the window, the icons and COM.
func (t *tray) destroy() {
	if !t.ending {
		t.removeIcon()
	}

	if t.timer {
		_, _, _ = procKillTimer.Call(t.hwnd, timerID)
		t.timer = false
	}

	if t.hwnd != 0 {
		_, _, _ = procDestroyWindow.Call(t.hwnd)
		t.hwnd = 0
	}

	t.destroyIcons()

	if t.coInit {
		_, _, _ = procCoUninitialize.Call()
		t.coInit = false
	}
}

// ownedError shows an error in a box the tray window owns. While it is
// open, the window is disabled and tray clicks are ignored.
func (t *tray) ownedError(text string) {
	t.modal = true
	defer func() { t.modal = false }()

	win.ShowErrorFor(t.hwnd, text)
}

func (t *tray) create() error {
	inst, _, _ := procGetModuleHandleW.Call(0)
	cls, title := win.UTF16Ptr(className), win.UTF16Ptr("SkyDemonConnect")

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

	t.timer = true

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
	name := win.UTF16Ptr("TaskbarCreated")
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

	if t.added && t.pending != nil {
		b := *t.pending
		t.pending = nil
		t.balloon(b.title, b.text, b.flags)
	}
}

// removeIcon takes the icon out of the notification area, also when an
// earlier failure left it unclear whether it is there.
func (t *tray) removeIcon() {
	t.shell(nimDelete, 0)
	t.added = false
}

// refresh updates icon and tooltip from the feed status. Only a change is
// sent to the notification area, or a retry while the icon isn't there:
// each call may wait on Explorer, which blocks this thread while it hangs.
func (t *tray) refresh() {
	st := t.opts.Feed.Status()
	handle, tip := t.icons[st.Kind], tooltip(st.String())

	t.dropStaleProblem(st)

	if !t.added || handle != t.shownIcon || tip != t.shownTip {
		t.nid.Icon = handle
		copyUTF16(t.nid.Tip[:], tip)
		t.showIcon()

		if t.added {
			t.shownIcon, t.shownTip = handle, tip
		}
	}

	if !t.status.update(st.Kind, st.Err) {
		return
	}

	t.opts.Log.Printf("tray: status: %s", st.String())

	if st.Kind == feed.Failed {
		t.balloon(problemTitle, st.Err, niifError)
	}
}

// dropStaleProblem forgets a queued problem balloon once st is no longer
// that problem: one that is over by the time the icon is there isn't news.
func (t *tray) dropStaleProblem(st feed.Status) {
	if p := t.pending; p != nil && p.flags == niifError &&
		(st.Kind != feed.Failed || st.Err != p.text) {
		t.pending = nil
	}
}

// balloonText is a balloon to show.
type balloonText struct {
	title, text string
	flags       uint32
}

// balloon shows a balloon at the icon. Without the icon, there is nothing
// to show it at: the latest one is kept until showIcon added the icon.
func (t *tray) balloon(title, text string, flags uint32) {
	t.nid.InfoFlags = flags

	copyUTF16(t.nid.InfoTitle[:], title)
	copyUTF16(t.nid.Info[:], text)

	if !t.added || !t.shell(nimModify, nifInfo) {
		t.pending = &balloonText{title: title, text: text, flags: flags}
	}
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
// process once the window procedure returns. The work is stopped first,
// and the icon is left to Windows: removing it is a call into Explorer,
// which is ending too and may stall, while Windows waits for no one.
func (t *tray) endSession() {
	t.opts.Log.Printf("tray: Windows is ending the session")
	t.ending = true

	if t.opts.SessionEnd != nil {
		t.opts.SessionEnd()
	}

	_, _, _ = procPostQuitMessage.Call(0)
}

// quit ends the message loop; Run's destroy then removes the icon.
func (t *tray) quit() { _, _, _ = procPostQuitMessage.Call(0) }

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

	p := win.UTF16Ptr(item.text)
	_, _, _ = procAppendMenuW.Call(handle, flags, uintptr(item.id), uintptr(unsafe.Pointer(p)))
}

func (t *tray) showMenu() {
	handle, _, _ := procCreatePopupMenu.Call()
	defer func() { _, _, _ = procDestroyMenu.Call(handle) }()

	state := &menuState{
		version:  t.opts.Version,
		status:   t.opts.Feed.Status().String(),
		canStart: filepath.IsAbs(t.opts.ExePath),
		hasLog:   t.opts.LogPath != "",
	}

	if t.opts.Destinations != nil {
		state.destinations = t.opts.Destinations()
	}

	if state.canStart {
		installs := t.installs()
		state.msfsFound = len(installs) > 0
		state.msfs = installs.State(t.opts.ExePath, t.opts.AutoArgs)
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

	verb, path := win.UTF16Ptr("open"), win.UTF16Ptr(t.opts.LogPath)

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

// scalingMaybeChanged redraws the icons if the display's scaling changed
// their size; Explorer doesn't always restart, with TaskbarCreated, then.
func (t *tray) scalingMaybeChanged() {
	if t.smallIconSize() == t.iconSize {
		return
	}

	err := t.makeIcons()
	if err != nil {
		t.opts.Log.Printf("tray: icons: %v", err)

		return
	}

	t.refresh()
}

// trayClick handles the icon's callback message: a click opens the menu,
// unless a box the tray owns is open; that click is dropped.
func (t *tray) trayClick(lparam uintptr) {
	if t.modal {
		return
	}

	switch lparam & loWordMask {
	case wmLButtonUp, wmRButtonUp:
		t.showMenu()
	}
}

// tick quits once opts.Quit is closed, and refreshes otherwise.
func (t *tray) tick() {
	select {
	case <-t.opts.Quit:
		t.quit()
	default:
		t.refresh()
	}
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
		t.trayClick(lparam)

		return 0
	case message == wmDisplayChange, message == wmSettingChange, message == wmDpiChanged:
		t.scalingMaybeChanged()
	case message == wmTimer:
		t.tick()

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
