//go:build windows

package win

import (
	"fmt"
	"runtime"
	"syscall"
	"unsafe"
)

const (
	wmQueryEndSession = 0x0011
	wmEndSession      = 0x0016
)

var (
	procGetModuleHandleW = kernel32.NewProc("GetModuleHandleW")
	procRegisterClassExW = user32.NewProc("RegisterClassExW")
	procCreateWindowExW  = user32.NewProc("CreateWindowExW")
	procDefWindowProcW   = user32.NewProc("DefWindowProcW")
	procGetMessageW      = user32.NewProc("GetMessageW")
	procDispatchMessageW = user32.NewProc("DispatchMessageW")
)

// wndClassEx is WNDCLASSEXW, as in internal/tray.
type wndClassEx struct {
	Size       uint32
	Style      uint32
	WndProc    uintptr
	ClsExtra   int32
	WndExtra   int32
	Instance   uintptr
	Icon       uintptr
	Cursor     uintptr
	Background uintptr
	MenuName   *uint16
	ClassName  *uint16
	IconSm     uintptr
}

// msg is MSG, as in internal/tray.
type msg struct {
	Hwnd    uintptr
	Message uint32
	WParam  uintptr
	LParam  uintptr
	Time    uint32
	Pt      struct{ X, Y int32 }
	Private uint32
}

// OnSessionEnd calls fn when Windows ends the session (logoff, shutdown or
// restart), before it ends the process; fn should return once the app has
// shut down. The console's control events don't do this: Windows doesn't
// send logoff or shutdown to a process that loaded user32.dll, as
// SimConnect.dll does. A hidden top-level window gets WM_ENDSESSION
// instead; a message-only window wouldn't. It returns an error if that
// window can't be created. Call it once.
func OnSessionEnd(fn func()) error {
	errc := make(chan error, 1)

	go func() {
		// The window belongs to this thread, which serves it as long as
		// the process lives.
		runtime.LockOSThread()

		err := sessionWindow(fn)
		errc <- err

		if err != nil {
			return
		}

		var message msg

		for {
			r, _, _ := procGetMessageW.Call(uintptr(unsafe.Pointer(&message)), 0, 0, 0)
			if int32(r) <= 0 { //nolint:gosec // GetMessage's BOOL is -1, 0 or more
				return
			}

			_, _, _ = procDispatchMessageW.Call(uintptr(unsafe.Pointer(&message)))
		}
	}()

	return <-errc
}

// sessionWindow creates the hidden window that calls fn on WM_ENDSESSION.
func sessionWindow(fn func()) error {
	proc := func(hwnd, message, wparam, lparam uintptr) uintptr {
		switch message {
		case wmQueryEndSession:
			return 1 // nothing to save: the session may end
		case wmEndSession:
			if wparam != 0 {
				fn()
			}

			return 0
		}

		r, _, _ := procDefWindowProcW.Call(hwnd, message, wparam, lparam)

		return r
	}

	inst, _, _ := procGetModuleHandleW.Call(0)
	cls := UTF16Ptr("SkyDemonConnectSession")

	wc := wndClassEx{WndProc: syscall.NewCallback(proc), Instance: inst, ClassName: cls}
	wc.Size = uint32(unsafe.Sizeof(wc))

	r, _, err := procRegisterClassExW.Call(uintptr(unsafe.Pointer(&wc)))
	if r == 0 {
		return fmt.Errorf("RegisterClassEx: %w", err)
	}

	// Top-level, never shown: no WS_VISIBLE, and no taskbar button.
	hwnd, _, err := procCreateWindowExW.Call(
		0, uintptr(unsafe.Pointer(cls)), 0, 0, 0, 0, 0, 0, 0, 0, inst, 0)
	if hwnd == 0 {
		return fmt.Errorf("CreateWindowEx: %w", err)
	}

	return nil
}
