//go:build windows

package win

import (
	"unsafe"
)

const (
	mbIconError     = 0x10
	mbIconInfo      = 0x40
	mbSetForeground = 0x10000
	mbTopmost       = 0x40000
)

// messageBox shows a modal message box owned by owner, or by nobody for 0.
// It comes to the front and stays on top: started by MSFS, the app has no
// window of its own, and a box behind a fullscreen MSFS would go unseen and
// keep the process waiting.
func messageBox(owner uintptr, text string, flags uintptr) {
	t, c := utf16Ptr(text), utf16Ptr("SkyDemonConnect")
	_, _, _ = procMessageBoxW.Call(owner, uintptr(unsafe.Pointer(t)), uintptr(unsafe.Pointer(c)),
		flags|mbSetForeground|mbTopmost)
}

// ShowError shows a modal error dialog.
func ShowError(text string) { messageBox(0, text, mbIconError) }

// ShowErrorFor shows a modal error dialog owned by the window owner, which
// it disables while it is open.
func ShowErrorFor(owner uintptr, text string) { messageBox(owner, text, mbIconError) }

// ShowInfo shows a modal information dialog.
func ShowInfo(text string) { messageBox(0, text, mbIconInfo) }
