package simconnect

import (
	"encoding/binary"
	"log"
	"strconv"
	"time"
)

// Message layout from SimConnect.h. The structures are packed.
const (
	recvException     = 1
	recvOpen          = 2
	recvQuit          = 3
	recvEvent         = 4
	recvSimobjectData = 8
	// recvSimobjectDataByType answers a request by object type, laid out
	// as recvSimobjectData.
	recvSimobjectDataByType = 9

	recvHeaderSize = 12 // SIMCONNECT_RECV: dwSize, dwVersion, dwID
	recvIDOffset   = 8
	// SIMCONNECT_RECV_EXCEPTION: the header, dwException, dwSendID, dwIndex.
	exceptionMinSize  = 16
	sendIDOffset      = 16
	indexOffset       = 20
	exceptionFullSize = 24
	// SIMCONNECT_RECV_EVENT: the header, uGroupID, uEventID, dwData.
	eventIDOffset   = 16
	eventDataOffset = 20
	eventMinSize    = 24
	// In SIMCONNECT_RECV_SIMOBJECT_DATA the header and 7 DWORDs precede
	// the data block: dwRequestID, dwObjectID, dwDefineID, ... and last
	// dwDefineCount.
	requestIDOffset   = 12
	defineIDOffset    = 20
	defineCountOffset = 36
	dataOffset        = 40
)

// The IDs of the one data definition and request.
const (
	defineID  = 1
	requestID = 1
)

// simGrace is how long after OPEN the Sim event is waited for before the
// session counts as live without it. MSFS may send it only after OPEN,
// and until then the position could be a menu's or the last flight's.
const simGrace = 500 * time.Millisecond

// exceptionKey identifies an exception: the code, and the argument the sim
// rejected. Not the send ID: that is new for each packet, so a probe that
// fails on every try would be logged every time.
type exceptionKey struct{ code, index uint32 }

// eventSim is the client event ID of the "Sim" system event: whether a
// flight is running (dwData 1) or not, sent on every change and once right
// after subscribing.
const eventSim = 1

// dispatcher handles the messages of one session and keeps what they told
// so far.
type dispatcher struct {
	log *log.Logger
	h   Handlers
	// exceptions holds the exceptions logged so far: one the sim repeats,
	// e.g. on every probe, is logged once.
	exceptions map[exceptionKey]bool
	// liveAt is when the session counts as live without a Sim event: the
	// end of simGrace after OPEN. It is zero once one came, or before
	// OPEN.
	liveAt time.Time
	// simKnown is set once a Sim event came.
	simKnown bool
	// connected is set by OPEN: the sim answered.
	connected bool
	// stopped is set while the Sim event reports no flight running: the
	// main menu, loading or the Esc menu. The first Sim event can come
	// before OPEN.
	stopped bool
	// traffic collects the answers to traffic requests.
	traffic trafficState
	// badData is set once a data block that doesn't match the definition
	// was logged: it repeats every frame.
	badData bool
	// badTraffic is badData for the traffic definition.
	badTraffic bool
	// airports collects the airport list.
	airports airportState
}

// handle processes one message and reports whether the sim quit.
func (d *dispatcher) handle(buf []byte) bool {
	if len(buf) < recvHeaderSize {
		return false
	}

	switch binary.LittleEndian.Uint32(buf[recvIDOffset:]) {
	case recvOpen:
		d.connected = true

		d.log.Printf("simconnect: connected")
		d.h.Connected(true)

		switch {
		case !d.simKnown:
			d.liveAt = time.Now().Add(simGrace)
		case !d.stopped:
			d.h.Live(true)
		}
	case recvQuit:
		return true
	case recvEvent:
		d.event(buf)
	case recvException:
		d.exception(buf)
	default:
		d.answer(binary.LittleEndian.Uint32(buf[recvIDOffset:]), buf)
	}

	return false
}

// answer handles the answers to requests: positions, traffic, airports.
func (d *dispatcher) answer(id uint32, buf []byte) {
	switch id {
	case recvSimobjectData:
		d.data(buf)
	case recvSimobjectDataByType:
		d.trafficData(buf)
	case recvAirportList:
		all := d.airports.add(buf, time.Now())
		if all != nil && d.h.Airports != nil {
			d.log.Printf("simconnect: %d airports", len(all))
			d.h.Airports(all)
		}
	}
}

// data handles a SIMCONNECT_RECV_SIMOBJECT_DATA. A block too short for
// every variable, e.g. because SimConnect rejected one renamed in an SDK
// update, would otherwise be dropped silently every frame, leaving the icon
// grey: it is logged, once per session, with the variable count SimConnect
// reports. That count is only logged, not checked: how MSFS 2024 fills it
// is not verified.
func (d *dispatcher) data(buf []byte) {
	var v [numVars]float64

	const want = dataOffset + numVars*8

	// Data for another request or definition isn't this one's layout.
	if len(buf) >= dataOffset &&
		(binary.LittleEndian.Uint32(buf[requestIDOffset:]) != requestID ||
			binary.LittleEndian.Uint32(buf[defineIDOffset:]) != defineID) {
		return
	}

	if len(buf) >= want {
		_, err := binary.Decode(buf[dataOffset:], binary.LittleEndian, &v)
		if err == nil {
			d.h.Update(toSample(&v))

			return
		}
	}

	if !d.badData {
		d.badData = true

		count := "no"
		if len(buf) >= dataOffset {
			count = strconv.Itoa(int(binary.LittleEndian.Uint32(buf[defineCountOffset:])))
		}

		d.log.Printf("simconnect: data block of %d bytes (%s variables), want %d bytes "+
			"(%d variables), not used", len(buf), count, want, numVars)
	}
}

// event handles a SIMCONNECT_RECV_EVENT.
func (d *dispatcher) event(buf []byte) {
	if len(buf) >= eventMinSize && binary.LittleEndian.Uint32(buf[eventIDOffset:]) == eventSim {
		d.sim(binary.LittleEndian.Uint32(buf[eventDataOffset:]) != 0)
	}
}

// tick makes the session live once simGrace passed after OPEN without a
// Sim event: a sim that doesn't send one would otherwise never be.
func (d *dispatcher) tick(now time.Time) {
	if d.liveAt.IsZero() || now.Before(d.liveAt) {
		return
	}

	d.liveAt = time.Time{}

	d.log.Printf("simconnect: no Sim event, taking a flight as running")
	d.h.Live(true)
}

// sim turns the Sim event into Live, so that the feed stops in the
// simulator's menus and starts again with the next flight.
func (d *dispatcher) sim(running bool) {
	d.stopped, d.simKnown, d.liveAt = !running, true, time.Time{}
	if !d.connected {
		return
	}

	if running {
		d.log.Printf("simconnect: flight running")

		// Asked for in the main menu, the airports may have come short or
		// not at all: once a flight runs, MSFS has them.
		d.airports.again(time.Now())
	} else {
		d.log.Printf("simconnect: no flight running (menu or loading)")
	}

	d.h.Live(running)
}

// exception logs a SIMCONNECT_RECV_EXCEPTION the first time its code comes
// up for its argument in the session.
func (d *dispatcher) exception(buf []byte) {
	if len(buf) < exceptionMinSize {
		return
	}

	key := exceptionKey{code: binary.LittleEndian.Uint32(buf[recvHeaderSize:])}
	if len(buf) >= exceptionFullSize {
		key.index = binary.LittleEndian.Uint32(buf[indexOffset:])
	}

	if d.exceptions[key] {
		return
	}

	if d.exceptions == nil {
		d.exceptions = map[exceptionKey]bool{}
	}

	d.exceptions[key] = true
	code := key.code

	// dwSendID and dwIndex tell which call, and which of its arguments,
	// the sim rejected.
	if len(buf) >= exceptionFullSize {
		d.log.Printf("simconnect: exception %d for send %d, argument %d (repeats are not logged)",
			code, binary.LittleEndian.Uint32(buf[sendIDOffset:]),
			binary.LittleEndian.Uint32(buf[indexOffset:]))

		return
	}

	d.log.Printf("simconnect: exception %d (repeats are not logged)", code)
}
