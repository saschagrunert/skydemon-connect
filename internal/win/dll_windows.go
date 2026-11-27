//go:build windows

package win

var procSetDefaultDllDirectories = kernel32.NewProc("SetDefaultDllDirectories")

// RestrictDLLSearch makes libraries that load other DLLs later, e.g.
// SimConnect.dll or shell extensions, find them in the exe's folder and
// System32 only, not in the working directory (MSFS's, with -auto) or PATH.
// Call it first thing, before anything shows a window.
func RestrictDLLSearch() {
	const searchDefaultDirs = 0x00001000 // LOAD_LIBRARY_SEARCH_DEFAULT_DIRS

	_, _, _ = procSetDefaultDllDirectories.Call(searchDefaultDirs)
}
