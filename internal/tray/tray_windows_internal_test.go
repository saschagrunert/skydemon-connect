//go:build windows

package tray

import (
	"fmt"
	"image"
	"log"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync/atomic"
	"syscall"
	"testing"
	"time"
	"unicode/utf16"
	"unsafe"

	"github.com/saschagrunert/skydemon-connect/internal/exexml"
	"github.com/saschagrunert/skydemon-connect/internal/feed"
	"github.com/saschagrunert/skydemon-connect/internal/icon"
)

func TestCopyUTF16(t *testing.T) {
	t.Parallel()

	for _, tc := range []struct {
		name string
		in   string
		size int
		want []uint16
	}{
		{"fits", "ab", 4, []uint16{'a', 'b', 0, 0}},
		{"exact", "abc", 4, []uint16{'a', 'b', 'c', 0}},
		{"truncated", "abcdef", 4, []uint16{'a', 'b', 'c', 0}},
		{"empty", "", 2, []uint16{0, 0}},
		{"NUL inside", "a\x00b", 3, []uint16{0, 0, 0}},
		// U+1F6E9 (small airplane) is the pair D83D DEE9.
		{"pair fits", "a\U0001F6E9", 4, []uint16{'a', 0xD83D, 0xDEE9, 0}},
		{"pair cut", "ab\U0001F6E9", 4, []uint16{'a', 'b', 0, 0}},
		{"pair before cut", "a\U0001F6E9b", 4, []uint16{'a', 0xD83D, 0xDEE9, 0}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			dst := make([]uint16, tc.size)
			for i := range dst {
				dst[i] = 'x' // stale content must be cleared
			}

			copyUTF16(dst, tc.in)

			if !slices.Equal(dst, tc.want) {
				t.Errorf("got %x, want %x (%q)", dst, tc.want, string(utf16.Decode(dst)))
			}
		})
	}
}

func TestAlreadyRunning(t *testing.T) {
	t.Parallel()

	name := fmt.Sprintf(`Local\SkyDemonConnectTest%d`, os.Getpid())

	if alreadyRunning(name) {
		t.Fatal("first call: already running")
	}

	if !alreadyRunning(name) {
		t.Error("second call: not running")
	}
}

// Every Win32 procedure exists: NewProc only looks a name up on the first
// call, which a typo would turn into a panic on a user's PC.
func TestProcsExist(t *testing.T) {
	t.Parallel()

	for _, p := range []*syscall.LazyProc{
		procRegisterClassExW, procCreateWindowExW, procDefWindowProcW, procGetMessageW,
		procTranslateMessage, procDispatchMessageW, procPostQuitMessage, procPostMessageW,
		procCreatePopupMenu, procAppendMenuW, procTrackPopupMenu, procDestroyMenu,
		procSetForegroundWindow, procGetCursorPos, procSetTimer, procMessageBoxW,
		procCreateIconIndirect, procGetSystemMetrics, procRegisterWindowMessageW,
		procSetProcessDPIAware, procShellNotifyIconW, procShellExecuteW, procCreateDIBSection,
		procCreateBitmap, procDeleteObject, procChangeWindowMsgFilter, procGetModuleHandleW,
		procCreateMutexW, procAllocConsole,
	} {
		err := p.Find()
		if err != nil {
			t.Error(err)
		}
	}
}

func TestMakeIcon(t *testing.T) {
	t.Parallel()

	h, err := makeIcon(icon.Render(16, icon.Green))
	if err != nil || h == 0 {
		t.Fatalf("icon %x: %v", h, err)
	}

	_, err = makeIcon(image.NewNRGBA(image.Rect(0, 0, 16, 8)))
	if err == nil {
		t.Error("a non-square image was accepted")
	}
}

// The feed's errors that are meant for the user fit the tooltip after its
// title.
func TestTooltipBudget(t *testing.T) {
	t.Parallel()

	room := len(notifyIconData{}.Tip) - 1 // the NUL

	st := feed.Status{Kind: feed.Failed, Err: strings.Repeat("x", feed.MaxErrLen)}
	if n := len(tooltipTitle + st.String()); n != room {
		t.Errorf("a %d-character error makes a %d-character tooltip, room for %d",
			feed.MaxErrLen, n, room)
	}
}

// Start with MSFS ticks and unticks: each click flips the entry.
func TestToggleMSFS(t *testing.T) {
	t.Parallel()

	installs := exexml.Installs{filepath.Join(t.TempDir(), "exe.xml")}
	clicked := &tray{
		opts: Options{
			ExePath:  `C:\Tools\SkyDemonConnect\SkyDemonConnect.exe`,
			MSFS:     installs,
			AutoArgs: []string{"-udp=auto,192.168.1.23:49002"},
			Log:      log.New(t.Output(), "", 0),
		},
		// A message box would wait for a click that never comes.
		showError: func(text string) { t.Error(text) },
	}

	for _, want := range []bool{true, false, true} {
		clicked.toggleMSFS()

		if got := installs.Enabled(clicked.opts.ExePath, clicked.opts.AutoArgs); got != want {
			t.Fatalf("after a click: enabled %v, want %v", got, want)
		}
	}
}

// Run creates the window and icons and shows the icon (on a runner without
// a desktop, Shell_NotifyIcon fails silently). When Windows ends the
// session, the tray lets it, calls SessionEnd once and returns. Run can
// only run once per process, so this is the one test of it.
func TestRun(t *testing.T) {
	t.Parallel()

	var ended atomic.Int32

	done := make(chan error, 1)

	go func() {
		done <- Run(&Options{
			Feed:       feed.New(feed.Outputs{}, log.New(t.Output(), "", 0)),
			Quit:       make(chan struct{}), // never closed
			Version:    "test",
			ExePath:    `C:\x.exe`,
			SessionEnd: func() { ended.Add(1) },
			Log:        log.New(t.Output(), "", 0),
			Auto:       true,
		})
	}()

	findWindow, sendMessage := user32.NewProc("FindWindowW"), user32.NewProc("SendMessageW")
	class := utf16Ptr(className)
	threadProcessID := user32.NewProc("GetWindowThreadProcessId")
	ours := func(hwnd uintptr) bool {
		var pid uint32

		_, _, _ = threadProcessID.Call(hwnd, uintptr(unsafe.Pointer(&pid)))

		return int(pid) == os.Getpid()
	}

	var hwnd uintptr

	for deadline := time.Now().Add(10 * time.Second); hwnd == 0; {
		select {
		case err := <-done:
			t.Fatalf("Run returned early: %v", err)
		default:
		}

		if time.Now().After(deadline) {
			t.Fatal("no tray window")
		}

		time.Sleep(10 * time.Millisecond)

		hwnd, _, _ = findWindow.Call(uintptr(unsafe.Pointer(class)), 0)
		if hwnd != 0 && !ours(hwnd) {
			// A running copy of the app, which mustn't get the messages.
			t.Skip("SkyDemonConnect is running on this PC")
		}
	}

	send := func(message, wparam uintptr) uintptr {
		r, _, _ := sendMessage.Call(hwnd, message, wparam, 0)

		return r
	}

	if r := send(wmQueryEndSession, 0); r != 1 {
		t.Errorf("WM_QUERYENDSESSION: %d, want 1 (the session may end)", r)
	}

	send(wmEndSession, 0) // the session goes on after all

	if n := ended.Load(); n != 0 {
		t.Errorf("SessionEnd called %d times for a session that goes on", n)
	}

	send(wmEndSession, 1)

	if n := ended.Load(); n != 1 {
		t.Errorf("SessionEnd called %d times, want once", n)
	}

	select {
	case err := <-done:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("Run did not return after the session ended")
	}
}
