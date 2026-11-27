//go:build windows

package serial

import (
	"fmt"
	"io"
	"strings"
	"syscall"
	"unsafe"
)

var (
	kernel32            = syscall.NewLazyDLL("kernel32.dll")
	procGetCommState    = kernel32.NewProc("GetCommState")
	procSetCommState    = kernel32.NewProc("SetCommState")
	procSetCommTimeouts = kernel32.NewProc("SetCommTimeouts")
	procCancelIoEx      = kernel32.NewProc("CancelIoEx")
)

const (
	// DCB flag bits: fBinary, fDtrControl = DTR_CONTROL_ENABLE,
	// fRtsControl = RTS_CONTROL_ENABLE.
	dcbBinary    = 1 << 0
	dcbDtrEnable = 1 << 4
	dcbRtsEnable = 1 << 12
	byteSize     = 8
	noParity     = 0
	oneStopBit   = 0
	xon          = 0x11
	xoff         = 0x13
)

// dcb is the Win32 DCB structure.
type dcb struct {
	DCBlength  uint32
	BaudRate   uint32
	Flags      uint32
	wReserved  uint16
	XonLim     uint16
	XoffLim    uint16
	ByteSize   byte
	Parity     byte
	StopBits   byte
	XonChar    byte
	XoffChar   byte
	ErrorChar  byte
	EofChar    byte
	EvtChar    byte
	wReserved1 uint16
}

// commTimeouts is the Win32 COMMTIMEOUTS structure.
type commTimeouts struct {
	ReadIntervalTimeout         uint32
	ReadTotalTimeoutMultiplier  uint32
	ReadTotalTimeoutConstant    uint32
	WriteTotalTimeoutMultiplier uint32
	WriteTotalTimeoutConstant   uint32
}

// Compile-time layout checks against the Windows SDK sizes.
var (
	_ [28]byte = [unsafe.Sizeof(dcb{})]byte{}
	_ [20]byte = [unsafe.Sizeof(commTimeouts{})]byte{}
)

type port struct{ h syscall.Handle }

func (p *port) Write(b []byte) (int, error) {
	var n uint32

	err := syscall.WriteFile(p.h, b, &n, nil)
	if err != nil {
		return int(n), fmt.Errorf("write: %w", err)
	}

	// A write timeout completes the write successfully, with fewer bytes.
	if int(n) < len(b) {
		return int(n), fmt.Errorf("write: %w (timeout)", io.ErrShortWrite)
	}

	return int(n), nil
}

func (p *port) Close() error {
	err := syscall.CloseHandle(p.h)
	if err != nil {
		return fmt.Errorf("close: %w", err)
	}

	return nil
}

// openPort opens the COM port name at baud, 8N1.
func openPort(name string, baud uint32) (io.WriteCloser, error) {
	path := name
	if !strings.HasPrefix(path, `\\.\`) {
		path = `\\.\` + name // required for COM10 and above
	}

	p, err := syscall.UTF16PtrFromString(path)
	if err != nil {
		return nil, fmt.Errorf("port name: %w", err)
	}

	h, err := syscall.CreateFile(p, syscall.GENERIC_READ|syscall.GENERIC_WRITE, 0, nil,
		syscall.OPEN_EXISTING, 0, 0)
	if err != nil {
		return nil, fmt.Errorf("open %s: %w", name, err)
	}

	err = configure(h, baud)
	if err != nil {
		_ = syscall.CloseHandle(h)

		return nil, fmt.Errorf("configure %s: %w", name, err)
	}

	return newPort(h), nil
}

// newPort wraps an open handle. Its Close doesn't wait for a write
// blocked in the driver: CancelIoEx with no OVERLAPPED cancels every I/O
// on the handle, also a synchronous write in another thread, and the
// handle is closed once that write returned.
func newPort(h syscall.Handle) io.WriteCloser {
	cancel := func() { _, _, _ = procCancelIoEx.Call(uintptr(h), 0) }

	return &idleCloser{port: &port{h: h}, cancel: cancel}
}

func configure(h syscall.Handle, baud uint32) error {
	var d dcb

	d.DCBlength = uint32(unsafe.Sizeof(d))

	if r, _, err := procGetCommState.Call(uintptr(h), uintptr(unsafe.Pointer(&d))); r == 0 {
		return fmt.Errorf("GetCommState: %w", err)
	}

	d.BaudRate = baud
	d.ByteSize, d.Parity, d.StopBits = byteSize, noParity, oneStopBit
	d.Flags = dcbBinary | dcbDtrEnable | dcbRtsEnable
	// XON/XOFF stay off, but SetCommState rejects equal characters, which
	// some virtual and Bluetooth drivers report as 0 and 0.
	d.XonChar, d.XoffChar = xon, xoff

	if r, _, err := procSetCommState.Call(uintptr(h), uintptr(unsafe.Pointer(&d))); r == 0 {
		return fmt.Errorf("SetCommState: %w", err)
	}

	timeouts := commTimeouts{
		WriteTotalTimeoutMultiplier: writeMultiplier(baud),
		WriteTotalTimeoutConstant:   writeSlackMS,
	}

	if r, _, err := procSetCommTimeouts.Call(
		uintptr(h),
		uintptr(unsafe.Pointer(&timeouts)),
	); r == 0 {
		return fmt.Errorf("SetCommTimeouts: %w", err)
	}

	return nil
}
