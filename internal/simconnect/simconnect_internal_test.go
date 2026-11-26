package simconnect

import (
	"encoding/hex"
	"errors"
	"fmt"
	"syscall"
	"testing"

	"github.com/saschagrunert/skydemon-connect/internal/feed"
	"github.com/saschagrunert/skydemon-connect/internal/position"
	"github.com/saschagrunert/skydemon-connect/internal/testutil"
)

func TestToSample(t *testing.T) {
	t.Parallel()

	for i, v := range simVars() {
		if v.name == "" || v.unit == "" {
			t.Errorf("simVars()[%d] is empty", i)
		}
	}

	var v [numVars]float64

	v[varLat], v[varLon], v[varAlt], v[varGS] = 51.4, 12.2, 762, 100
	v[varVelEast], v[varVelNorth] = 0, -150 // southbound
	v[varHeading] = 370
	v[varPitch], v[varBank] = 3, 20 // MSFS: nose down 3°, left bank 20°

	s := toSample(&v)
	if s.Lat != 51.4 || s.Lon != 12.2 || s.AltM != 762 || s.GSKnots != 100 {
		t.Errorf("position fields: %+v", s)
	}

	if s.TrackTrue != 180 || s.HeadingTrue != 10 {
		t.Errorf("track %v heading %v, want 180 and 10", s.TrackTrue, s.HeadingTrue)
	}

	if s.Pitch != -3 || s.Roll != -20 {
		t.Errorf("pitch %v roll %v, want -3 (down) and -20 (left)", s.Pitch, s.Roll)
	}
}

// goldenData is a SIMCONNECT_RECV_SIMOBJECT_DATA message written out by
// hand from SimConnect.h, rather than from the offsets under test: lat
// 51.5, lon 12.25, ground speed 100 kt, every other variable 0.
func goldenData(t *testing.T) []byte {
	t.Helper()

	const fields = "" +
		"70000000" + // dwSize, 112 bytes
		"04000000" + // dwVersion
		"08000000" + // dwID: SIMCONNECT_RECV_ID_SIMOBJECT_DATA
		"01000000" + // dwRequestID
		"01000000" + // dwObjectID
		"01000000" + // dwDefineID
		"00000000" + // dwFlags
		"01000000" + // dwentrynumber
		"01000000" + // dwoutof
		"09000000" + // dwDefineCount
		"0000000000c04940" + // PLANE LATITUDE 51.5
		"0000000000802840" + // PLANE LONGITUDE 12.25
		"0000000000000000" + // PLANE ALTITUDE
		"0000000000005940" + // GROUND VELOCITY 100
		"0000000000000000" + // VELOCITY WORLD X
		"0000000000000000" + // VELOCITY WORLD Z
		"0000000000000000" + // PLANE HEADING DEGREES TRUE
		"0000000000000000" + // PLANE PITCH DEGREES
		"0000000000000000" // PLANE BANK DEGREES

	b, err := hex.DecodeString(fields)
	if err != nil {
		t.Fatal(err)
	}

	return b
}

func TestHandle(t *testing.T) {
	t.Parallel()

	var (
		live    []bool
		updates []position.Sample
	)

	d := &dispatcher{log: testutil.Log(t), h: Handlers{
		Update: func(s position.Sample) { updates = append(updates, s) },
		Live:   func(l bool) { live = append(live, l) },
		Ended:  func() { t.Error("Ended is not called by handle") },
		Failed: func(error) { t.Error("Failed is not called by handle") },
	}}

	if d.handle([]byte{1, 2, 3}) {
		t.Error("a short buffer is not a quit")
	}

	if d.handle(msgOpen()) || !d.connected || len(live) != 1 || !live[0] {
		t.Errorf("open: connected=%v live=%v", d.connected, live)
	}

	if d.handle(msgException(7)) {
		t.Error("an exception is not a quit")
	}

	if d.handle(msgEvent(eventSim, 0)[:eventMinSize-1]) || len(live) != 1 {
		t.Errorf("a truncated event must be ignored: %v", live)
	}

	data := goldenData(t)
	if d.handle(data) || len(updates) != 1 ||
		updates[0].Lat != 51.5 || updates[0].Lon != 12.25 || updates[0].GSKnots != 100 {
		t.Errorf("data: %+v", updates)
	}

	if d.handle(data[:len(data)-1]) || len(updates) != 1 {
		t.Error("a truncated data block must be ignored")
	}

	if !d.handle(msgQuit()) {
		t.Error("quit not reported")
	}
}

func TestLoadFailure(t *testing.T) {
	t.Parallel()

	for _, tc := range []struct {
		err  error
		want error
	}{
		{fmt.Errorf("LoadLibraryEx: %w", syscall.Errno(errModNotFound)), errVCRuntime},
		{fmt.Errorf("LoadLibraryEx: %w", syscall.Errno(errBadExeFormat)), errNot64Bit},
		{fmt.Errorf("LoadLibraryEx: %w", syscall.Errno(5)), errCantLoad},
	} {
		got := loadFailure(tc.err)
		if !errors.Is(got, tc.want) {
			t.Errorf("loadFailure(%v) = %v, want %v", tc.err, got, tc.want)
		}
	}
}

// The errors the user sees fit the tray's tooltip, where the feed's status
// shows them.
func TestUserErrorsFitTheTooltip(t *testing.T) {
	t.Parallel()

	for _, err := range []error{errNotFound, errVCRuntime, errNot64Bit, errCantLoad, errOldDLL} {
		if n := len(err.Error()); n > feed.MaxErrLen {
			t.Errorf("%q: %d characters, room for %d", err, n, feed.MaxErrLen)
		}
	}
}

func TestHR(t *testing.T) {
	t.Parallel()

	for _, tc := range []struct {
		r    uintptr
		fail bool
	}{
		{0, false},          // S_OK
		{1, false},          // S_FALSE
		{0x80004005, true},  // E_FAIL
		{0xFFFFFFFF, true},  // -1
		{0x7FFFFFFF, false}, // largest success code
	} {
		err := hr(tc.r)
		if (err != nil) != tc.fail {
			t.Errorf("hr(%#x) = %v, want failure %v", tc.r, err, tc.fail)
		}
	}

	err := hr(0x80004005)
	if got := err.Error(); got != "HRESULT 0x80004005" || !errors.Is(err, errNoSim) {
		t.Errorf("E_FAIL: %q", got)
	}
}
