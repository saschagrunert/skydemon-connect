package simconnect

import (
	"encoding/binary"
	"log"
)

// Message layout from SimConnect.h. The structures are packed.
const (
	recvException     = 1
	recvOpen          = 2
	recvQuit          = 3
	recvEvent         = 4
	recvSimobjectData = 8

	recvHeaderSize   = 12 // SIMCONNECT_RECV: dwSize, dwVersion, dwID
	recvIDOffset     = 8
	exceptionMinSize = 16
	// SIMCONNECT_RECV_EVENT: the header, uGroupID, uEventID, dwData.
	eventIDOffset   = 16
	eventDataOffset = 20
	eventMinSize    = 24
	// In SIMCONNECT_RECV_SIMOBJECT_DATA the header and 7 DWORDs precede
	// the data block.
	dataOffset = 40
)

// eventSim is the client event ID of the "Sim" system event: whether a
// flight is running (dwData 1) or not, sent on every change and once right
// after subscribing.
const eventSim = 1

// dispatcher handles the messages of one session and keeps what they told
// so far.
type dispatcher struct {
	log *log.Logger
	h   Handlers
	// exceptions holds the exception codes logged so far: one the sim
	// repeats, e.g. on every probe, is logged once.
	exceptions map[uint32]bool
	// connected is set by OPEN: the sim answered.
	connected bool
	// stopped is set while the Sim event reports no flight running: the
	// main menu, loading or the Esc menu. The first Sim event can come
	// before OPEN.
	stopped bool
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

		if !d.stopped {
			d.h.Live(true)
		}
	case recvQuit:
		return true
	case recvEvent:
		d.event(buf)
	case recvException:
		d.exception(buf)
	case recvSimobjectData:
		var v [numVars]float64

		_, err := binary.Decode(buf[min(len(buf), dataOffset):], binary.LittleEndian, &v)
		if err != nil {
			return false
		}

		d.h.Update(toSample(&v))
	}

	return false
}

// event handles a SIMCONNECT_RECV_EVENT.
func (d *dispatcher) event(buf []byte) {
	if len(buf) >= eventMinSize && binary.LittleEndian.Uint32(buf[eventIDOffset:]) == eventSim {
		d.sim(binary.LittleEndian.Uint32(buf[eventDataOffset:]) != 0)
	}
}

// sim turns the Sim event into Live, so that the feed stops in the
// simulator's menus and starts again with the next flight.
func (d *dispatcher) sim(running bool) {
	d.stopped = !running
	if !d.connected {
		return
	}

	if running {
		d.log.Printf("simconnect: flight running")
	} else {
		d.log.Printf("simconnect: no flight running (menu or loading)")
	}

	d.h.Live(running)
}

// exception logs a SIMCONNECT_RECV_EXCEPTION the first time its code
// comes up in the session.
func (d *dispatcher) exception(buf []byte) {
	if len(buf) < exceptionMinSize {
		return
	}

	code := binary.LittleEndian.Uint32(buf[recvHeaderSize:])
	if d.exceptions[code] {
		return
	}

	if d.exceptions == nil {
		d.exceptions = map[uint32]bool{}
	}

	d.exceptions[code] = true
	d.log.Printf("simconnect: exception %d (repeats are not logged)", code)
}
