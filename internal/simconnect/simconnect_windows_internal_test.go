//go:build windows

package simconnect

import (
	"errors"
	"os"
	"path/filepath"
	"slices"
	"syscall"
	"testing"
)

func TestDLLCandidates(t *testing.T) {
	t.Parallel()

	env := map[string]string{"MSFS2024_SDK": `D:\SDK 2024`, "MSFS_SDK": `relative\ignored`}
	c := dllCandidates(`C:\Tools\SkyDemonConnect`, func(k string) string { return env[k] })

	want := []string{
		`C:\Tools\SkyDemonConnect\SimConnect.dll`,
		`D:\SDK 2024\SimConnect SDK\lib\SimConnect.dll`,
	}
	if !slices.Equal(c, want) {
		t.Errorf("candidates %q, want %q", c, want)
	}

	// Without a usable exe folder or SDK variable, there is none: nothing
	// under C:\ that other users could have put there.
	if c := dllCandidates("", func(string) string { return "" }); len(c) != 0 {
		t.Errorf("candidates %q", c)
	}
}

func TestLoadDLLNotFound(t *testing.T) {
	t.Parallel()

	missing := filepath.Join(t.TempDir(), "SimConnect.dll")

	_, err := loadDLL([]string{missing})

	var setup *setupError
	if !errors.As(err, &setup) || !errors.Is(setup.user, errNotFound) {
		t.Errorf("got %v, want errNotFound", err)
	}
}

// A 32-bit DLL is reported as not the 64-bit SimConnect.dll.
func TestLoadDLLWrongArchitecture(t *testing.T) {
	t.Parallel()

	dll32 := filepath.Join(os.Getenv("SystemRoot"), "SysWOW64", "kernel32.dll")

	_, err := loadLibrary(dll32)
	if errors.Is(err, syscall.ERROR_FILE_NOT_FOUND) ||
		errors.Is(err, syscall.ERROR_PATH_NOT_FOUND) {
		t.Skipf("no 32-bit kernel32.dll: %v", err)
	}

	if !errors.Is(err, syscall.Errno(errBadExeFormat)) {
		t.Fatalf("got %v, want error %d", err, errBadExeFormat)
	}

	_, err = loadDLL([]string{dll32})

	var setup *setupError
	if !errors.As(err, &setup) || !errors.Is(setup.user, errNot64Bit) {
		t.Errorf("got %v, want errNot64Bit", err)
	}
}

// The first DLL that exists is final: when it doesn't load, the next one
// is not tried, although it would load.
func TestLoadDLLFirstIsFinal(t *testing.T) {
	t.Parallel()

	broken := filepath.Join(t.TempDir(), "SimConnect.dll")

	err := os.WriteFile(broken, []byte("not a DLL"), 0o600)
	if err != nil {
		t.Fatal(err)
	}

	good := filepath.Join(os.Getenv("SystemRoot"), "System32", "version.dll")

	dll, err := loadDLL([]string{broken, good})
	if err == nil {
		_ = dll.Release()

		t.Fatal("loaded the second DLL")
	}

	if _, ok := errors.AsType[*setupError](err); !ok {
		t.Errorf("got %v, want a setup error", err)
	}
}

// A 64-bit DLL that isn't SimConnect lacks its functions.
func TestBindWrongDLL(t *testing.T) {
	t.Parallel()

	dll, err := loadLibrary(filepath.Join(os.Getenv("SystemRoot"), "System32", "version.dll"))
	if err != nil {
		t.Skipf("no version.dll: %v", err)
	}

	_, err = bind(dll)

	var setup *setupError
	if !errors.As(err, &setup) || !errors.Is(setup.user, errOldDLL) {
		t.Errorf("got %v, want errOldDLL", err)
	}
}

// The kernel32 functions are looked up lazily: a typo in a name would only
// show when it is first used. SimConnect's are bound when the DLL loads.
func TestProcsExist(t *testing.T) {
	t.Parallel()

	for _, p := range []*syscall.LazyProc{procLoadLibraryExW, procCreateEventW} {
		err := p.Find()
		if err != nil {
			t.Errorf("%s: %v", p.Name, err)
		}
	}
}

// The real SimConnect.dll, which CI's weekly bundle job takes from the MSFS
// SDK and names in SKYDEMONCONNECT_SIMCONNECT_DLL: every function the app
// uses is bound, and without MSFS an open fails with the error the app
// takes for "MSFS isn't running". Skipped without the variable.
func TestRealDLL(t *testing.T) {
	t.Parallel()

	path := os.Getenv("SKYDEMONCONNECT_SIMCONNECT_DLL")
	if path == "" {
		t.Skip("SKYDEMONCONNECT_SIMCONNECT_DLL not set")
	}

	dll, err := loadDLL([]string{path})
	if err != nil {
		t.Fatal(err)
	}

	a, err := bind(dll)
	if err != nil {
		t.Fatal(err)
	}

	c, err := a.open("SkyDemonConnect test")
	if err == nil {
		c.close()
		t.Fatal("open worked without MSFS")
	}

	// errNoSim (E_FAIL) is what the app takes for "MSFS isn't running".
	if !errors.Is(err, errNoSim) {
		t.Errorf("open without MSFS: %v, want %v", err, errNoSim)
	}
}
