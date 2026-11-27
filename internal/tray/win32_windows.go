//go:build windows

package tray

import (
	"errors"
	"fmt"
	"image"
	"syscall"
	"unsafe"
)

// Minimal Win32 bindings for the tray UI.

var (
	user32   = syscall.NewLazyDLL("user32.dll")
	shell32  = syscall.NewLazyDLL("shell32.dll")
	gdi32    = syscall.NewLazyDLL("gdi32.dll")
	kernel32 = syscall.NewLazyDLL("kernel32.dll")
	ole32    = syscall.NewLazyDLL("ole32.dll")

	procRegisterClassExW       = user32.NewProc("RegisterClassExW")
	procCreateWindowExW        = user32.NewProc("CreateWindowExW")
	procDefWindowProcW         = user32.NewProc("DefWindowProcW")
	procGetMessageW            = user32.NewProc("GetMessageW")
	procTranslateMessage       = user32.NewProc("TranslateMessage")
	procDispatchMessageW       = user32.NewProc("DispatchMessageW")
	procPostQuitMessage        = user32.NewProc("PostQuitMessage")
	procPostMessageW           = user32.NewProc("PostMessageW")
	procCreatePopupMenu        = user32.NewProc("CreatePopupMenu")
	procAppendMenuW            = user32.NewProc("AppendMenuW")
	procTrackPopupMenu         = user32.NewProc("TrackPopupMenu")
	procDestroyMenu            = user32.NewProc("DestroyMenu")
	procSetForegroundWindow    = user32.NewProc("SetForegroundWindow")
	procGetCursorPos           = user32.NewProc("GetCursorPos")
	procSetTimer               = user32.NewProc("SetTimer")
	procKillTimer              = user32.NewProc("KillTimer")
	procDestroyWindow          = user32.NewProc("DestroyWindow")
	procCreateIconIndirect     = user32.NewProc("CreateIconIndirect")
	procGetSystemMetrics       = user32.NewProc("GetSystemMetrics")
	procRegisterWindowMessageW = user32.NewProc("RegisterWindowMessageW")
	procGetDpiForWindow        = user32.NewProc("GetDpiForWindow")
	procGetSystemMetricsForDpi = user32.NewProc("GetSystemMetricsForDpi")
	procDestroyIcon            = user32.NewProc("DestroyIcon")
	procShellNotifyIconW       = shell32.NewProc("Shell_NotifyIconW")
	procShellExecuteW          = shell32.NewProc("ShellExecuteW")
	procCreateDIBSection       = gdi32.NewProc("CreateDIBSection")
	procCreateBitmap           = gdi32.NewProc("CreateBitmap")
	procDeleteObject           = gdi32.NewProc("DeleteObject")
	procChangeWindowMsgFilter  = user32.NewProc("ChangeWindowMessageFilterEx")
	procGetModuleHandleW       = kernel32.NewProc("GetModuleHandleW")
	procCoInitializeEx         = ole32.NewProc("CoInitializeEx")
	procCoUninitialize         = ole32.NewProc("CoUninitialize")
)

const (
	wmNull            = 0x0000
	wmClose           = 0x0010
	wmQueryEndSession = 0x0011
	wmEndSession      = 0x0016
	wmSettingChange   = 0x001A
	wmDisplayChange   = 0x007E
	wmTimer           = 0x0113
	wmDpiChanged      = 0x02E0
	wmLButtonUp       = 0x0202
	wmRButtonUp       = 0x0205
	wmApp             = 0x8000
	wmTray            = wmApp + 1

	nimAdd    = 0
	nimModify = 1
	nimDelete = 2

	nifMessage = 0x01
	nifIcon    = 0x02
	nifTip     = 0x04
	nifInfo    = 0x10

	niifInfo  = 1
	niifError = 3

	mfString    = 0x000
	mfGrayed    = 0x001
	mfChecked   = 0x008
	mfSeparator = 0x800

	tpmRightButton = 0x0002
	tpmNoNotify    = 0x0080
	tpmReturnCmd   = 0x0100

	// COINIT_APARTMENTTHREADED | COINIT_DISABLE_OLE1DDE, as ShellExecute
	// wants it.
	coinitShell = 0x2 | 0x4

	smCxSmIcon       = 49
	swShowNormal     = 1
	msgfltAllow      = 1
	highSurrogateMin = 0xD800
	lowSurrogateMin  = 0xDC00
	loWordMask       = 0xFFFF
	bitmapHeaderSize = 40
	bitsPerPixel     = 32
	bitsPerMaskWord  = 16
	bytesPerMaskWord = 2
)

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

type point struct{ X, Y int32 }

type msg struct {
	Hwnd    uintptr
	Message uint32
	WParam  uintptr
	LParam  uintptr
	Time    uint32
	Pt      point
	Private uint32
}

// notifyIconData is NOTIFYICONDATAW in its Vista+ layout.
type notifyIconData struct {
	Size            uint32
	Wnd             uintptr
	ID              uint32
	Flags           uint32
	CallbackMessage uint32
	Icon            uintptr
	Tip             [128]uint16
	State           uint32
	StateMask       uint32
	Info            [256]uint16
	TimeoutVersion  uint32
	InfoTitle       [64]uint16
	InfoFlags       uint32
	GUIDItem        [16]byte
	BalloonIcon     uintptr
}

// Compile-time layout checks against the Windows SDK sizes on amd64.
var (
	_ [976]byte = [unsafe.Sizeof(notifyIconData{})]byte{}
	_ [80]byte  = [unsafe.Sizeof(wndClassEx{})]byte{}
	_ [48]byte  = [unsafe.Sizeof(msg{})]byte{}
	// BITMAPINFO: the 40-byte header and one bmiColors entry.
	_ [44]byte = [unsafe.Sizeof(bitmapInfoHeader{})]byte{}
	_ [32]byte = [unsafe.Sizeof(iconInfo{})]byte{}
)

type bitmapInfoHeader struct {
	Size          uint32
	Width         int32
	Height        int32
	Planes        uint16
	BitCount      uint16
	Compression   uint32
	SizeImage     uint32
	XPelsPerMeter int32
	YPelsPerMeter int32
	ClrUsed       uint32
	ClrImportant  uint32
	_             [4]byte // bmiColors[1]
}

type iconInfo struct {
	FIcon    int32
	XHotspot uint32
	YHotspot uint32
	HbmMask  uintptr
	HbmColor uintptr
}

// utf16Ptr converts s for a Win32 call. Our strings never contain NUL;
// if one did, the call gets an empty string.
func utf16Ptr(s string) *uint16 {
	p, err := syscall.UTF16PtrFromString(s)
	if err != nil {
		return &[]uint16{0}[0]
	}

	return p
}

// copyUTF16 copies s into a fixed buffer, truncating and NUL-terminating.
// A truncation never leaves half a surrogate pair behind.
func copyUTF16(dst []uint16, s string) {
	u, err := syscall.UTF16FromString(s)
	if err != nil {
		u = []uint16{0}
	}

	if len(u) > len(dst) {
		u = u[:len(dst)]
		u[len(u)-1] = 0

		// A high surrogate whose low half was cut off.
		if n := len(u); n >= 2 && u[n-2] >= highSurrogateMin && u[n-2] < lowSurrogateMin {
			u[n-2] = 0
		}
	}

	copy(dst, u)
	clear(dst[len(u):])
}

var errIconSize = errors.New("icon: not square")

// deleteObject frees a GDI object.
func deleteObject(h uintptr) { _, _, _ = procDeleteObject.Call(h) }

// makeIcon turns a size×size image into an HICON with real alpha.
func makeIcon(img *image.NRGBA) (uintptr, error) {
	b := img.Bounds()
	if b.Dx() != b.Dy() {
		return 0, fmt.Errorf("%w: %v", errIconSize, b.Size())
	}

	size := b.Dx()

	header := bitmapInfoHeader{
		Size:     bitmapHeaderSize,
		Width:    int32(size),  //nolint:gosec // icon sizes are small
		Height:   -int32(size), //nolint:gosec // see above; negative: top-down
		Planes:   1,
		BitCount: bitsPerPixel,
	}

	var bits unsafe.Pointer

	color, _, err := procCreateDIBSection.Call(0, uintptr(unsafe.Pointer(&header)), 0,
		uintptr(unsafe.Pointer(&bits)), 0, 0)
	if color == 0 {
		return 0, fmt.Errorf("CreateDIBSection: %w", err)
	}
	defer deleteObject(color)

	// A 32-bit DIB is BGRA, top-down here; the image is RGBA, both with
	// straight alpha.
	dib := unsafe.Slice((*byte)(bits), len(img.Pix))
	for i := 0; i+3 < len(img.Pix); i += 4 {
		dib[i], dib[i+1], dib[i+2], dib[i+3] = img.Pix[i+2], img.Pix[i+1], img.Pix[i], img.Pix[i+3]
	}

	mask := make([]byte, (size+bitsPerMaskWord-1)/bitsPerMaskWord*bytesPerMaskWord*size)

	hmask, _, err := procCreateBitmap.Call(
		uintptr(size),
		uintptr(size),
		1,
		1,
		uintptr(unsafe.Pointer(&mask[0])),
	)
	if hmask == 0 {
		return 0, fmt.Errorf("CreateBitmap: %w", err)
	}
	defer deleteObject(hmask)

	ii := iconInfo{FIcon: 1, HbmMask: hmask, HbmColor: color}

	h, _, err := procCreateIconIndirect.Call(uintptr(unsafe.Pointer(&ii)))
	if h == 0 {
		return 0, fmt.Errorf("CreateIconIndirect: %w", err)
	}

	return h, nil
}
