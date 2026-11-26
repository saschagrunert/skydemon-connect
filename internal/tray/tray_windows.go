//go:build windows

package tray

import (
	"fmt"
	"log"
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
)

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
	// AutoArgs are the options "Start with MSFS" writes after -auto,
	// quoted for the Windows command line.
	AutoArgs []string
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
}

// tooltipTitle is the tooltip's first line; the feed status follows.
const tooltipTitle = "SkyDemonConnect\n"

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

	_, _, _ = procSetProcessDPIAware.Call() // crisp icons on scaled displays

	t := &tray{opts: *opts, icons: map[feed.Kind]uintptr{}, showError: ShowError}

	err := t.create()
	if err != nil {
		return err
	}

	// No greeting when MSFS started the app, nor before a problem balloon.
	if !opts.Auto && t.status.last != feed.Failed {
		t.balloon(welcomeTitle, welcomeText, niifInfo)
	}

	t.loop()

	return nil
}

func (t *tray) create() error {
	size, _, _ := procGetSystemMetrics.Call(smCxSmIcon)
	if size == 0 {
		size = 16
	}

	for _, kind := range kinds() {
		h, err := makeIcon(icon.Render(int(size), colour(kind)))
		if err != nil {
			return err
		}

		t.icons[kind] = h
	}

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

	t.nid.Size = uint32(unsafe.Sizeof(t.nid))
	t.nid.Wnd = hwnd
	t.nid.ID = iconID
	t.nid.CallbackMessage = wmTray
	t.refresh(nimAdd, nifMessage|nifIcon|nifTip)

	if r, _, err := procSetTimer.Call(hwnd, timerID, tickMS, 0); r == 0 {
		return fmt.Errorf("SetTimer: %w", err)
	}

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

func (t *tray) shell(op uintptr, flags uint32) {
	t.nid.Flags = flags
	_, _, _ = procShellNotifyIconW.Call(op, uintptr(unsafe.Pointer(&t.nid)))
}

// refresh updates icon and tooltip from the feed status.
func (t *tray) refresh(op uintptr, flags uint32) {
	st := t.opts.Feed.Status()
	t.nid.Icon = t.icons[st.Kind]
	copyUTF16(t.nid.Tip[:], tooltipTitle+st.String())
	t.shell(op, flags)

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
func (t *tray) session(message, ending uintptr) uintptr {
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
	t.shell(nimDelete, 0)

	if t.opts.SessionEnd != nil {
		t.opts.SessionEnd()
	}

	_, _, _ = procPostQuitMessage.Call(0)
}

func (t *tray) quit() {
	t.shell(nimDelete, 0)

	_, _, _ = procPostQuitMessage.Call(0)
}

func (t *tray) loop() {
	var message msg

	for {
		r, _, _ := procGetMessageW.Call(uintptr(unsafe.Pointer(&message)), 0, 0, 0)
		if int32(r) <= 0 { //nolint:gosec // BOOL, -1 on error
			return
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

	items := menu(
		t.opts.Version,
		t.opts.Feed.Status().String(),
		t.installs().Enabled(t.opts.ExePath, t.opts.AutoArgs),
	)
	for _, item := range items {
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
	// Unticked also when the entry is for a moved exe or other options:
	// enabling fixes it.
	installs := t.installs()
	enable := !installs.Enabled(t.opts.ExePath, t.opts.AutoArgs)

	err := installs.Set(enable, t.opts.ExePath, t.opts.AutoArgs)
	if err != nil {
		t.opts.Log.Printf("tray: exe.xml: %v", err)
		t.showError("Could not update MSFS exe.xml:\n" + err.Error())

		return
	}

	t.opts.Log.Printf("tray: start with MSFS: %v (%s %v)", enable, t.opts.ExePath, t.opts.AutoArgs)

	if enable {
		t.balloon(addedTitle, addedText, niifInfo)
	} else {
		t.balloon(removedTitle, removedText, niifInfo)
	}
}

// wndProc is the window procedure. Run creates the window once per
// process, so the callback, of which Windows allows a limited number, is
// made once too.
func (t *tray) wndProc(hwnd, message, wparam, lparam uintptr) uintptr {
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
			t.refresh(nimModify, nifIcon|nifTip)
		}

		return 0
	case message == wmClose:
		t.quit()

		return 0
	case message == wmQueryEndSession, message == wmEndSession:
		return t.session(message, wparam)
	case t.taskbarCreated != 0 && message == t.taskbarCreated:
		t.refresh(nimAdd, nifMessage|nifIcon|nifTip) // Explorer restarted

		return 0
	}

	r, _, _ := procDefWindowProcW.Call(hwnd, message, wparam, lparam)

	return r
}
