// Package simconnect reads the user aircraft from Microsoft Flight Simulator
// 2024 through the native SimConnect.dll, without cgo.
package simconnect

import (
	"errors"
	"fmt"
	"math"
	"syscall"

	"github.com/saschagrunert/skydemon-connect/internal/position"
)

// Handlers receive what happens on the connection. All are called from the
// connection goroutine, must not block, and must all be set but Traffic.
type Handlers struct {
	// Update receives every sim frame's aircraft state.
	Update func(position.Sample)
	// Traffic receives the other aircraft within 30 NM about once a
	// second, and nil when the session ends. Nil: no traffic is asked for.
	Traffic func([]position.Traffic)
	// Connected is called with each change of whether a session is
	// connected to MSFS, in a flight or not, so that the UI can tell "MSFS
	// not running" from "in its menu". It starts as false.
	Connected func(connected bool)
	// Live is called with each change of whether the sim is live:
	// connected and, as its Sim event reports, in a flight rather than in
	// a menu or loading. It starts as false, so the first call is
	// Live(true). An active pause (the Pause key) is expected to stay live.
	Live func(live bool)
	// Ended is called when a session that had connected ends: the sim
	// closed or crashed.
	Ended func()
	// Failed is called with a problem the user must fix, such as a
	// missing SimConnect.dll, in words for the tray, and with nil once it
	// is solved. Connecting is retried meanwhile.
	Failed func(err error)
}

// What the user is told about a SimConnect.dll that can't be used. The
// details, such as the path and the Windows error, go to the log. Each
// fits the tray tooltip, with its "SkyDemonConnect\nError: " prefix.
var (
	errNotFound = errors.New(
		"SimConnect.dll not found: keep it next to SkyDemonConnect.exe, as in the zip")
	errVCRuntime = errors.New("SimConnect.dll can't be loaded: " +
		"install the Microsoft Visual C++ runtime (vc_redist.x64.exe)")
	errNot64Bit = errors.New("SimConnect.dll is not the 64-bit one from the MSFS 2024 SDK")
	errCantLoad = errors.New("SimConnect.dll can't be loaded, see the log")
	errOldDLL   = errors.New("SimConnect.dll lacks functions: use the one from the MSFS 2024 SDK")
)

// Windows error codes of LoadLibraryEx.
const (
	errModNotFound  = 126 // a DLL it imports, e.g. the VC++ runtime, is missing
	errBadExeFormat = 193 // not a 64-bit DLL
)

// setupError is a problem the user must fix: user is what they are told,
// detail what the log says.
type setupError struct {
	user, detail error
}

func (e *setupError) Error() string { return e.detail.Error() }

func (e *setupError) Unwrap() error { return e.detail }

// loadFailure turns a LoadLibraryEx error into what the user is told.
func loadFailure(err error) error {
	switch {
	case errors.Is(err, syscall.Errno(errModNotFound)):
		return errVCRuntime
	case errors.Is(err, syscall.Errno(errBadExeFormat)):
		return errNot64Bit
	}

	return errCantLoad
}

// hresultError is a failed HRESULT, as SimConnect's functions return.
type hresultError uint32

// errNoSim is E_FAIL, which SimConnect_Open returns while MSFS isn't
// running.
const errNoSim hresultError = 0x80004005

// hresultFailure is an HRESULT's sign bit, set for failures.
const hresultFailure = 1 << 31

func (e hresultError) Error() string { return fmt.Sprintf("HRESULT 0x%08X", uint32(e)) }

// hr turns an HRESULT, a 32-bit value returned through a uintptr, into an
// error.
func hr(r uintptr) error {
	code := uint32(r & math.MaxUint32)
	if code&hresultFailure != 0 {
		return hresultError(code)
	}

	return nil
}

const (
	unitDegrees       = "degrees"
	unitFeetPerSecond = "feet per second"
)

// simVar is one simulation variable of the data definition.
type simVar struct {
	name, unit string
}

// simVars returns the data definition; its order is the layout of the
// data block SimConnect returns, indexed by the var* constants.
func simVars() [numVars]simVar {
	return [numVars]simVar{
		varLat:      {"PLANE LATITUDE", unitDegrees},
		varLon:      {"PLANE LONGITUDE", unitDegrees},
		varAlt:      {"PLANE ALTITUDE", "meters"},
		varGS:       {"GROUND VELOCITY", "knots"},
		varVelEast:  {"VELOCITY WORLD X", unitFeetPerSecond},
		varVelNorth: {"VELOCITY WORLD Z", unitFeetPerSecond},
		varHeading:  {"PLANE HEADING DEGREES TRUE", unitDegrees},
		varPitch:    {"PLANE PITCH DEGREES", unitDegrees}, // MSFS: nose down positive
		varBank:     {"PLANE BANK DEGREES", unitDegrees},  // MSFS: left bank positive
	}
}

const (
	varLat = iota
	varLon
	varAlt
	varGS
	varVelEast
	varVelNorth
	varHeading
	varPitch
	varBank

	numVars
)

// minTrackSpeedKnots: below this ground speed the velocity vector is mostly
// noise, so the heading stands in for the track.
const minTrackSpeedKnots = 2.0

// toSample converts the raw sim variables to a Sample in output
// conventions (pitch up and roll right positive). GROUND VELOCITY can come
// out a hair below zero when standing; a speed is never negative.
func toSample(v *[numVars]float64) position.Sample {
	heading := position.Norm360(v[varHeading])
	speed := math.Abs(v[varGS])

	return position.Sample{
		Lat:         v[varLat],
		Lon:         v[varLon],
		AltM:        v[varAlt],
		GSKnots:     speed,
		TrackTrue:   trackFromVelocity(v[varVelEast], v[varVelNorth], speed, heading),
		HeadingTrue: heading,
		Pitch:       -v[varPitch],
		Roll:        -v[varBank],
	}
}

// trackFromVelocity returns the true ground track in degrees from the world
// east and north velocity components. Below about 2 kt the track is noise,
// so headingTrue is returned instead.
func trackFromVelocity(east, north, gsKnots, headingTrue float64) float64 {
	if gsKnots < minTrackSpeedKnots {
		return position.Norm360(headingTrue)
	}

	return position.Norm360(math.Atan2(east, north) * position.DegPerRad)
}
