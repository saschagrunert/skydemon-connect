//go:build windows

package tray

import "unsafe"

// messageBox shows a modal message box. It comes to the front and stays on
// top: started by MSFS, the app has no window of its own, and a box behind
// a fullscreen MSFS would go unseen and keep the process waiting.
func messageBox(text string, flags uintptr) {
	t, c := utf16Ptr(text), utf16Ptr("SkyDemonConnect")
	_, _, _ = procMessageBoxW.Call(0, uintptr(unsafe.Pointer(t)), uintptr(unsafe.Pointer(c)),
		flags|mbSetForeground|mbTopmost)
}

// ShowError shows a modal error dialog.
func ShowError(text string) { messageBox(text, mbIconError) }

// ShowInfo shows a modal information dialog.
func ShowInfo(text string) { messageBox(text, mbIconInfo) }
