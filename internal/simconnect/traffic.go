package simconnect

import (
	"bytes"
	"encoding/binary"
	"fmt"
	"math"
	"slices"
	"strings"
	"time"

	"github.com/saschagrunert/skydemon-connect/internal/position"
)

// Traffic: every aircraft and helicopter MSFS simulates near the user's,
// whoever created it (MSFS's AI, injectors such as BeyondATC or FSLTL),
// asked for as a second data definition by object type.
const (
	trafficDefineID = 2
	// firstTrafficRequestID is the request ID of the first traffic
	// request; each gets the next, see trafficState.
	firstTrafficRequestID = 2
	objectTypeAircraft    = 2 // SIMCONNECT_SIMOBJECT_TYPE_AIRCRAFT
	objectTypeHelicopter  = 3 // SIMCONNECT_SIMOBJECT_TYPE_HELICOPTER

	// trafficRadiusM is how far out traffic is asked for: 30 NM, about
	// what an EFB's traffic display shows.
	trafficRadiusM = 30 * 1852
	// trafficInterval is how often it is asked for, as often as traffic
	// receivers report a target.
	trafficInterval = time.Second
	// trafficTimeout is how long an answer is waited for before it is
	// asked for again. Until then the sim stalls, e.g. loading scenery,
	// and the last traffic is kept; after, it is dropped as stale.
	trafficTimeout = 5 * time.Second

	datatypeFloat64  = 4 // SIMCONNECT_DATATYPE_FLOAT64
	datatypeString32 = 6 // SIMCONNECT_DATATYPE_STRING32
	string32Len      = 32

	// In SIMCONNECT_RECV_SIMOBJECT_DATA(_BYTYPE): dwObjectID, and
	// dwentrynumber and dwoutof, which number the objects of one answer.
	objectIDOffset = 16
	entryOffset    = 28
	outOfOffset    = 32

	// maxCallsign is the length of an ADS-B callsign.
	maxCallsign = 8
	// maxICAO is the highest 24-bit ICAO address.
	maxICAO = 1<<24 - 1
)

const (
	tLat = iota
	tLon
	tAlt
	tVS
	tGS
	tVelEast
	tVelNorth
	tHeading
	tOnGround
	tUser

	numTrafficFloats
)

// trafficVar is one variable of the traffic definition.
type trafficVar struct {
	name, unit string
	datatype   uint32
}

// trafficVars returns the traffic definition: the floats indexed by the t*
// constants, then the ATC ID (the registration or callsign) as a string.
func trafficVars() []trafficVar {
	f := func(name, unit string) trafficVar { return trafficVar{name, unit, datatypeFloat64} }

	return []trafficVar{
		tLat:      f("PLANE LATITUDE", unitDegrees),
		tLon:      f("PLANE LONGITUDE", unitDegrees),
		tAlt:      f("PLANE ALTITUDE", "meters"),
		tVS:       f("VERTICAL SPEED", "feet per minute"),
		tGS:       f("GROUND VELOCITY", "knots"),
		tVelEast:  f("VELOCITY WORLD X", unitFeetPerSecond),
		tVelNorth: f("VELOCITY WORLD Z", unitFeetPerSecond),
		tHeading:  f("PLANE HEADING DEGREES TRUE", unitDegrees),
		tOnGround: f("SIM ON GROUND", "bool"),
		tUser:     f("IS USER SIM", "bool"),
		{"ATC ID", "", datatypeString32},
	}
}

// trafficDataLen is the size of one object's data block.
const trafficDataLen = numTrafficFloats*8 + string32Len

// callsign cleans the ATC ID for an EFB: printable ASCII without commas,
// which separate the fields of XTRAFFIC, at most 8 characters. It is ""
// when nothing is left.
func callsign(raw []byte) string {
	if i := bytes.IndexByte(raw, 0); i >= 0 {
		raw = raw[:i]
	}

	var b strings.Builder

	for _, c := range strings.TrimSpace(string(raw)) {
		if c > ' ' && c < 0x7F && c != ',' && b.Len() < maxCallsign {
			b.WriteRune(c)
		}
	}

	return b.String()
}

// toTraffic converts one object's data block, without its ICAO address;
// ok is false for the user's own aircraft and for objects without a
// position.
func toTraffic(data []byte) (position.Traffic, bool) {
	if len(data) < trafficDataLen {
		return position.Traffic{}, false
	}

	var v [numTrafficFloats]float64

	_, err := binary.Decode(data, binary.LittleEndian, &v)
	if err != nil || v[tUser] != 0 || slices.ContainsFunc(v[:], notFinite) {
		return position.Traffic{}, false
	}

	// Only to test for 0°/0°, which an object without a position has.
	at := position.Sample{
		Lat: v[tLat], Lon: v[tLon],
		AltM: 0, GSKnots: 0, TrackTrue: 0, HeadingTrue: 0, Pitch: 0, Roll: 0,
	}
	if at.IsNullIsland() {
		return position.Traffic{}, false
	}

	heading := position.Norm360(v[tHeading])
	speed := math.Abs(v[tGS])

	return position.Traffic{
		Callsign:  callsign(data[numTrafficFloats*8 : trafficDataLen]),
		Lat:       v[tLat],
		Lon:       v[tLon],
		AltM:      v[tAlt],
		VSFpm:     v[tVS],
		TrackTrue: trackFromVelocity(v[tVelEast], v[tVelNorth], speed, heading),
		GSKnots:   speed,
		ICAO:      0,
		Airborne:  v[tOnGround] == 0,
	}, true
}

func notFinite(f float64) bool { return math.IsNaN(f) || math.IsInf(f, 0) }

// trafficKind is the traffic of one object type: the request asked for
// last, and the last complete answer.
type trafficKind struct {
	askedAt time.Time
	// got collects the answer to request id, one message per object,
	// numbered entry of outOf; done is set once the last one came.
	got    []position.Traffic
	latest []position.Traffic
	// id is the request asked for last, 0 before the first.
	id, objectType uint32
	done           bool
}

// trafficState is the traffic of a session. Each request gets its own ID,
// so that a late answer to an earlier one isn't taken for the current
// one's, and the next one is only asked for once the last one was
// answered.
type trafficState struct {
	// icao holds the address given to each object ID.
	icao  map[uint32]uint32
	kinds []trafficKind
	// nextID is the next request ID, lastICAO the address given last.
	nextID, lastICAO uint32
}

func (ts *trafficState) init() {
	if ts.kinds != nil {
		return
	}

	ts.kinds = []trafficKind{{objectType: objectTypeAircraft}, {objectType: objectTypeHelicopter}}
	ts.icao = map[uint32]uint32{}
}

// due asks with ask, for each object type, for the objects of that type
// if the last answer is trafficInterval old, or none came for
// trafficTimeout. The bool reports whether the latest traffic changed.
func (ts *trafficState) due(now time.Time, ask func(id, objectType uint32) error) (bool, error) {
	ts.init()

	changed := false

	for i := range ts.kinds {
		kind := &ts.kinds[i]
		age := now.Sub(kind.askedAt)

		switch {
		case kind.id == 0, kind.done && age >= trafficInterval:
		case !kind.done && age >= trafficTimeout:
			if len(kind.latest) > 0 {
				kind.latest, changed = nil, true
			}
		default:
			continue
		}

		// The request ID of the own aircraft's data is never reused.
		ts.nextID = max(ts.nextID, firstTrafficRequestID)
		kind.id, kind.askedAt, kind.got, kind.done = ts.nextID, now, nil, false
		ts.nextID++

		err := ask(kind.id, kind.objectType)
		if err != nil {
			return changed, err
		}
	}

	return changed, nil
}

// add adds object objectID, entry of outOf of the answer to request id;
// the bool reports whether that completed the answer. Answers to earlier
// requests are ignored.
func (ts *trafficState) add(id, entry, outOf, objectID uint32, t position.Traffic, ok bool) bool {
	for i := range ts.kinds {
		kind := &ts.kinds[i]
		if kind.id != id || kind.done {
			continue
		}

		if ok && outOf > 0 {
			t.ICAO = ts.address(objectID)
			if t.Callsign == "" {
				// Something to label the target with, unique like
				// the address.
				t.Callsign = fmt.Sprintf("T%06X", t.ICAO)
			}

			kind.got = append(kind.got, t)
		}

		if entry < outOf {
			return false
		}

		kind.done, kind.latest = true, kind.got

		return true
	}

	return false
}

// all is the latest traffic of every object type.
func (ts *trafficState) all() []position.Traffic {
	var out []position.Traffic

	for _, k := range ts.kinds {
		out = append(out, k.latest...)
	}

	return out
}

// address returns the ICAO address of objectID, given out in turn: the
// object IDs' low 24 bits could be the same for two objects, or 0.
func (ts *trafficState) address(objectID uint32) uint32 {
	a, ok := ts.icao[objectID]
	if !ok {
		ts.lastICAO = ts.lastICAO%maxICAO + 1
		a = ts.lastICAO
		ts.icao[objectID] = a
	}

	return a
}

// trafficData handles a SIMCONNECT_RECV_SIMOBJECT_DATA_BYTYPE.
func (d *dispatcher) trafficData(buf []byte) {
	if d.h.Traffic == nil || len(buf) < dataOffset ||
		binary.LittleEndian.Uint32(buf[defineIDOffset:]) != trafficDefineID {
		return
	}

	outOf := binary.LittleEndian.Uint32(buf[outOfOffset:])
	t, ok := toTraffic(buf[dataOffset:])

	// A variable the sim rejected, which it reports only as an exception,
	// shortens every block: each target would be dropped silently.
	if outOf > 0 && len(buf)-dataOffset < trafficDataLen && !d.badTraffic {
		d.badTraffic = true

		d.log.Printf("simconnect: traffic data block of %d bytes (%d variables), want %d bytes "+
			"(%d variables), not used", len(buf)-dataOffset,
			binary.LittleEndian.Uint32(buf[defineCountOffset:]), trafficDataLen, len(trafficVars()))
	}

	if d.traffic.add(binary.LittleEndian.Uint32(buf[requestIDOffset:]),
		binary.LittleEndian.Uint32(buf[entryOffset:]), outOf,
		binary.LittleEndian.Uint32(buf[objectIDOffset:]), t, ok) {
		d.h.Traffic(d.traffic.all())
	}
}
