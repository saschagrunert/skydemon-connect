//go:build windows

package simconnect

import (
	"context"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"syscall"
	"time"
	"unsafe"
)

// Constants from SimConnect.h.
const (
	datatypeFloat64 = 4
	periodSimFrame  = 3
	objectUser      = 0
	unused          = 0xFFFFFFFF
)

// LoadLibraryEx flags: look for the DLL's own imports, e.g. the VC++
// runtime, next to it, in the exe's folder and in System32, never in the
// current directory or on PATH. Given to LoadLibraryEx, they replace the
// process's default from SetDefaultDllDirectories for this load.
const (
	loadLibrarySearchDLLLoadDir  = 0x00000100
	loadLibrarySearchDefaultDirs = 0x00001000
)

var (
	kernel32           = syscall.NewLazyDLL("kernel32.dll")
	procLoadLibraryExW = kernel32.NewProc("LoadLibraryExW")
	procCreateEventW   = kernel32.NewProc("CreateEventW")
)

type api struct {
	procOpen, procClose, procAddToDataDefinition, procRequestData,
	procSubscribeToSystemEvent, procGetNextDispatch *syscall.Proc
}

// dllCandidates are the places SimConnect.dll is looked for, in order:
// exeDir (where the exe is) and the SDK folders the environment names.
// All are absolute, as LoadLibraryEx requires.
func dllCandidates(exeDir string, getenv func(string) string) []string {
	var c []string

	if filepath.IsAbs(exeDir) {
		c = append(c, filepath.Join(exeDir, "SimConnect.dll"))
	}

	for _, env := range []string{"MSFS2024_SDK", "MSFS_SDK"} {
		if d := getenv(env); filepath.IsAbs(d) {
			c = append(c, filepath.Join(d, "SimConnect SDK", "lib", "SimConnect.dll"))
		}
	}

	// No fallbacks in folders directly under C:\, such as the SDK's
	// default C:\MSFS 2024 SDK: other users of the PC can put files there,
	// and an elevated MSFS would start us, and load their DLL, as admin.
	return c
}

// exeDir is the folder of the running exe, or "" if unknown.
func exeDir() string {
	exe, err := os.Executable()
	if err != nil {
		return ""
	}

	return filepath.Dir(exe)
}

// loadLibrary loads the DLL at the absolute path without the default
// search order, so that a SimConnect.dll planted in the current directory
// or on PATH is never picked up.
func loadLibrary(path string) (*syscall.DLL, error) {
	p, err := syscall.UTF16PtrFromString(path)
	if err != nil {
		return nil, fmt.Errorf("path: %w", err)
	}

	h, _, err := procLoadLibraryExW.Call(uintptr(unsafe.Pointer(p)), 0,
		loadLibrarySearchDLLLoadDir|loadLibrarySearchDefaultDirs)
	if h == 0 {
		return nil, fmt.Errorf("LoadLibraryEx: %w", err)
	}

	return &syscall.DLL{Name: path, Handle: syscall.Handle(h)}, nil
}

// loadDLL loads the first of candidates that exists. That one is final:
// when it fails to load, its error is returned, and no later candidate is
// tried. A DLL next to the exe, as the zip ships it, is then never swapped
// for an SDK folder's, which may be one other users of the PC can write
// to, e.g. C:\MSFS 2024 SDK.
func loadDLL(candidates []string) (*syscall.DLL, error) {
	for _, p := range candidates {
		_, err := os.Stat(p)
		if err != nil {
			continue
		}

		dll, err := loadLibrary(p)
		if err != nil {
			return nil, &setupError{
				user:   loadFailure(err),
				detail: fmt.Errorf("load %s: %w", p, err),
			}
		}

		return dll, nil
	}

	return nil, &setupError{
		user:   errNotFound,
		detail: fmt.Errorf("%w; looked in %s", errNotFound, strings.Join(candidates, ", ")),
	}
}

// bind looks up the functions used. A DLL that lacks one is released.
func bind(dll *syscall.DLL) (*api, error) {
	a := &api{}
	for _, p := range []struct {
		dst  **syscall.Proc
		name string
	}{
		{&a.procOpen, "SimConnect_Open"},
		{&a.procClose, "SimConnect_Close"},
		{&a.procAddToDataDefinition, "SimConnect_AddToDataDefinition"},
		{&a.procRequestData, "SimConnect_RequestDataOnSimObject"},
		{&a.procSubscribeToSystemEvent, "SimConnect_SubscribeToSystemEvent"},
		{&a.procGetNextDispatch, "SimConnect_GetNextDispatch"},
	} {
		var err error

		*p.dst, err = dll.FindProc(p.name)
		if err != nil {
			_ = dll.Release()

			return nil, &setupError{user: errOldDLL, detail: fmt.Errorf("%s: %w", dll.Name, err)}
		}
	}

	return a, nil
}

// Run connects to the sim and reconnects until ctx is done. A missing or
// broken SimConnect.dll is reported through h.Failed and looked for again
// with every retry, so that copying it in takes effect without a restart.
func Run(ctx context.Context, l *log.Logger, h Handlers) {
	// A SimConnect handle must be used from a single OS thread.
	runtime.LockOSThread()

	defer runtime.UnlockOSThread()

	o := &dllOpener{log: l}
	loop(ctx, l, o.open, h)
}

// dllOpener opens connections through SimConnect.dll, loading it first.
type dllOpener struct {
	log *log.Logger
	a   *api
}

func (o *dllOpener) open(name string) (conn, error) {
	if o.a == nil {
		dll, err := loadDLL(dllCandidates(exeDir(), os.Getenv))
		if err != nil {
			return nil, err
		}

		a, err := bind(dll)
		if err != nil {
			return nil, err
		}

		// Logged once it is usable: a DLL that bind rejects is loaded
		// again on every retry.
		o.log.Printf("simconnect: using %s", dll.Name)
		o.a = a
	}

	// Not returned directly: a nil *dllConn in a conn is not nil.
	c, err := o.a.open(name)
	if err != nil {
		return nil, err
	}

	return c, nil
}

// dllConn is a connection through SimConnect.dll. SimConnect signals
// event whenever a message is queued, so waiting needs no polling.
type dllConn struct {
	a      *api
	handle uintptr
	event  syscall.Handle
}

func (a *api) open(name string) (*dllConn, error) {
	n, err := syscall.BytePtrFromString(name)
	if err != nil {
		return nil, fmt.Errorf("client name: %w", err)
	}

	// Auto-reset, initially not signalled.
	event, _, err := procCreateEventW.Call(0, 0, 0, 0)
	if event == 0 {
		return nil, fmt.Errorf("CreateEvent: %w", err)
	}

	c := &dllConn{a: a, event: syscall.Handle(event)}

	r, _, _ := a.procOpen.Call(
		uintptr(unsafe.Pointer(&c.handle)),
		uintptr(unsafe.Pointer(n)),
		0, // no window
		0, // no window message
		event,
		0, // default configuration
	)

	err = hr(r)
	if err != nil {
		_ = syscall.CloseHandle(c.event)

		return nil, err
	}

	return c, nil
}

func (c *dllConn) addToDataDefinition(name, unit string) error {
	n, err := syscall.BytePtrFromString(name)
	if err != nil {
		return fmt.Errorf("sim var name: %w", err)
	}

	u, err := syscall.BytePtrFromString(unit)
	if err != nil {
		return fmt.Errorf("sim var unit: %w", err)
	}

	r, _, _ := c.a.procAddToDataDefinition.Call(c.handle, defineID,
		uintptr(unsafe.Pointer(n)), uintptr(unsafe.Pointer(u)),
		datatypeFloat64, 0 /* epsilon 0.0f */, unused)

	return hr(r)
}

func (c *dllConn) requestData() error {
	r, _, _ := c.a.procRequestData.Call(
		c.handle,
		requestID,
		defineID,
		objectUser,
		periodSimFrame,
		0,
		0,
		0,
		0,
	)

	return hr(r)
}

func (c *dllConn) subscribe(event uint32, name string) error {
	n, err := syscall.BytePtrFromString(name)
	if err != nil {
		return fmt.Errorf("event name: %w", err)
	}

	r, _, _ := c.a.procSubscribeToSystemEvent.Call(c.handle, uintptr(event),
		uintptr(unsafe.Pointer(n)))

	return hr(r)
}

func (c *dllConn) nextDispatch() []byte {
	var (
		p *byte
		n uint32
	)

	r, _, _ := c.a.procGetNextDispatch.Call(
		c.handle,
		uintptr(unsafe.Pointer(&p)),
		uintptr(unsafe.Pointer(&n)),
	)
	if hr(r) != nil || p == nil {
		return nil
	}

	return unsafe.Slice(p, n)
}

func (c *dllConn) wait() {
	const ms = uint32(maxWait / time.Millisecond)

	_, _ = syscall.WaitForSingleObject(c.event, ms)
}

func (c *dllConn) close() {
	_, _, _ = c.a.procClose.Call(c.handle)
	_ = syscall.CloseHandle(c.event)
}
