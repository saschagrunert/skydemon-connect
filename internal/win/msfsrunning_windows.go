//go:build windows

package win

import (
	"syscall"
	"unsafe"
)

// MSFSRunning reports whether MSFS runs on this PC, by any user. MSFS 2020
// counts too: by the executable's name alone, it can't be told from MSFS
// 2024's Microsoft Store build. When the
// processes can't be listed, it says so: then nothing is changed under it.
func MSFSRunning() bool {
	snap, err := syscall.CreateToolhelp32Snapshot(syscall.TH32CS_SNAPPROCESS, 0)
	if err != nil {
		return true
	}

	defer func() { _ = syscall.CloseHandle(snap) }()

	var entry syscall.ProcessEntry32

	entry.Size = uint32(unsafe.Sizeof(entry))

	for err = syscall.Process32First(snap, &entry); err == nil; err = syscall.Process32Next(snap, &entry) {
		if isMSFS(syscall.UTF16ToString(entry.ExeFile[:])) {
			return true
		}
	}

	return false
}
