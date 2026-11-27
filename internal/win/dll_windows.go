//go:build windows

package win

var procSetDefaultDllDirectories = kernel32.NewProc("SetDefaultDllDirectories")

// RestrictDLLSearch makes a DLL loaded by name, e.g. by a shell extension
// or later by SimConnect.dll, come from System32 only: not from the
// working directory (MSFS's, with -auto), PATH, or the exe's folder, which
// may be a Downloads folder full of other files. SimConnect.dll itself is
// loaded by its full path with flags of its own, which this doesn't
// change. Call it first thing, before anything shows a window.
func RestrictDLLSearch() {
	const searchSystem32 = 0x00000800 // LOAD_LIBRARY_SEARCH_SYSTEM32

	_, _, _ = procSetDefaultDllDirectories.Call(searchSystem32)
}
