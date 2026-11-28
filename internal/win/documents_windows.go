//go:build windows

package win

import (
	"errors"
	"fmt"
	"syscall"
	"unsafe"
)

var errNoDocuments = errors.New("no Documents folder")

var (
	shell32 = syscall.NewLazyDLL("shell32.dll")
	ole32   = syscall.NewLazyDLL("ole32.dll")

	procSHGetKnownFolderPath = shell32.NewProc("SHGetKnownFolderPath")
	procCoTaskMemFree        = ole32.NewProc("CoTaskMemFree")
)

// FOLDERID_Documents, {FDD39AD0-238F-46AF-ADB4-6C85480369C7}.
const (
	documentsData1 = 0xFDD39AD0
	documentsData2 = 0x238F
	documentsData3 = 0x46AF
)

// Documents is the user's Documents folder, wherever it is: OneDrive and
// folder redirection move it away from the profile.
func Documents() (string, error) {
	id := syscall.GUID{
		Data1: documentsData1,
		Data2: documentsData2,
		Data3: documentsData3,
		Data4: [8]byte{0xAD, 0xB4, 0x6C, 0x85, 0x48, 0x03, 0x69, 0xC7},
	}

	var p *uint16

	r, _, _ := procSHGetKnownFolderPath.Call(uintptr(unsafe.Pointer(&id)), 0, 0,
		uintptr(unsafe.Pointer(&p)))

	// The path is allocated even on failure, and freed by the caller.
	defer func() { _, _, _ = procCoTaskMemFree.Call(uintptr(unsafe.Pointer(p))) }()

	if r != 0 {
		return "", fmt.Errorf("%w: SHGetKnownFolderPath failed with %#x", errNoDocuments, r)
	}

	return utf16PtrToString(p), nil
}

// utf16PtrToString reads the NUL-terminated string at p.
func utf16PtrToString(p *uint16) string {
	n := 0
	for ptr := unsafe.Pointer(p); *(*uint16)(ptr) != 0; n++ {
		ptr = unsafe.Add(ptr, unsafe.Sizeof(*p))
	}

	return syscall.UTF16ToString(unsafe.Slice(p, n))
}
