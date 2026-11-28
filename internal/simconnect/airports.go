package simconnect

import (
	"bytes"
	"encoding/binary"
	"math"
	"strings"
	"time"

	"github.com/saschagrunert/skydemon-connect/internal/position"
)

// The airports of the whole world, asked for once a session with
// SimConnect_RequestAllFacilities: about 40,000, in packets of
// SIMCONNECT_RECV_AIRPORT_LIST.
const (
	recvAirportList = 18 // SIMCONNECT_RECV_ID_AIRPORT_LIST
	// airportsRequestID is far from the traffic requests' IDs, which
	// count up; each request of the airports adds one, so that a late
	// answer to an earlier one is told apart.
	airportsRequestID = 0x7FFF0000

	// SIMCONNECT_RECV_FACILITIES_LIST: the header, dwRequestID,
	// dwArraySize, dwEntryNumber (the packet, from 0) and dwOutOf (the
	// packets), then the array.
	listRequestOffset = 12
	listSizeOffset    = 16
	listEntryOffset   = 20
	listOutOfOffset   = 24
	listDataOffset    = 28
	// SIMCONNECT_DATA_FACILITY_AIRPORT ends in Latitude, Longitude and
	// Altitude; before them is the ident, char[9] and the region char[3]
	// in MSFS 2024, 36 bytes packed. Another size is taken from the
	// packet, so that another ident length doesn't shift the positions.
	airportDoubles  = 3 * 8
	airportSize     = 9 + 3 + airportDoubles
	maxAirportIdent = 9
	// maxTail is how much a packet may have beyond its elements, as
	// padding.
	maxTail = 8
	// maxAirportPackets bounds the packets kept for one list: far more
	// than 40,000 airports need.
	maxAirportPackets = 4096
	// minAirports is the least a whole world has: fewer, e.g. while MSFS is
	// in its main menu, or only its cache, is asked for again when a
	// flight starts.
	minAirports = 1000
	// airportsLost is when a request without a packet of its answer for
	// that long counts as lost, e.g. to an exception, and is asked again,
	// up to maxLostAirports times a session and once more at each flight
	// start. A short answer is asked again at up to maxShortAirports
	// flight starts.
	airportsLost     = time.Minute
	maxLostAirports  = 3
	maxShortAirports = 2
)

// airportState collects the packets of the airport list.
type airportState struct {
	packets map[uint32][]position.Airport
	// outOf is the packet count, done set once all came.
	outOf uint32
	done  bool
	// asked is set while a request is out or answered; whole once a list
	// of at least minAirports came. heard is when the request went out or
	// a packet of its answer came.
	asked, whole bool
	heard        time.Time
	// id is the last request's, requests how many were made, lost and
	// short how many were asked again without an answer or after a short
	// one.
	id, requests uint32
	lost, short  int
}

// nextID is the ID of the next request.
func (as *airportState) nextID() uint32 { return airportsRequestID + as.requests }

// ask starts collecting the answer to the request nextID gave.
func (as *airportState) ask(now time.Time) {
	as.id = as.nextID()
	as.requests++
	as.packets, as.outOf, as.done, as.asked, as.heard = nil, 0, false, true, now
}

// again, as a flight starts, lets the next tick ask again after a short
// answer, a few times, or a lost request, and lets a request that may
// still be answered be asked again once it counts as lost.
func (as *airportState) again(now time.Time) {
	switch {
	case !as.asked || as.whole:
	case as.done:
		if as.short < maxShortAirports {
			as.short++
			as.asked = false
		}
	case as.isLost(now):
		as.asked = false
	default:
		as.lost = min(as.lost, maxLostAirports-1)
	}
}

// retryLost lets the next tick ask again when the last request got no
// answer, a few times a session: MSFS may not answer at all.
func (as *airportState) retryLost(now time.Time) {
	if as.asked && as.isLost(now) && as.lost < maxLostAirports {
		as.lost++
		as.asked = false
	}
}

// isLost reports whether the last request got nothing for airportsLost.
func (as *airportState) isLost(now time.Time) bool {
	return !as.done && now.Sub(as.heard) >= airportsLost
}

// listHeader is the SIMCONNECT_RECV_FACILITIES_LIST part of a packet: the
// array size, the packet's number and the packet count.
type listHeader struct{ n, entry, outOf uint32 }

// readListHeader reads the header of a packet of the airport request id;
// ok is false for another request's or a broken one.
func readListHeader(buf []byte, id uint32) (listHeader, bool) {
	if len(buf) < listDataOffset ||
		binary.LittleEndian.Uint32(buf[listRequestOffset:]) != id {
		return listHeader{n: 0, entry: 0, outOf: 0}, false
	}

	h := listHeader{
		n:     binary.LittleEndian.Uint32(buf[listSizeOffset:]),
		entry: binary.LittleEndian.Uint32(buf[listEntryOffset:]),
		outOf: binary.LittleEndian.Uint32(buf[listOutOfOffset:]),
	}

	return h, h.outOf > 0 && h.outOf <= maxAirportPackets && h.entry < h.outOf
}

// add takes one SIMCONNECT_RECV_AIRPORT_LIST, come at now, and returns the
// whole list once its last packet came, else nil.
func (as *airportState) add(buf []byte, now time.Time) []position.Airport {
	h, ok := readListHeader(buf, as.id)
	if !as.asked || as.done || !ok {
		return nil
	}

	if as.packets == nil {
		as.packets, as.outOf = map[uint32][]position.Airport{}, h.outOf
	}

	if h.outOf != as.outOf {
		return nil // not this list's
	}

	as.packets[h.entry] = airportsOf(buf[listDataOffset:], h.n)
	as.heard = now

	if len(as.packets) < int(as.outOf) {
		return nil
	}

	as.done = true

	var all []position.Airport

	for i := range as.outOf {
		all = append(all, as.packets[i]...)
	}

	as.packets = nil
	as.whole = len(all) >= minAirports

	return all
}

// airportsOf reads n SIMCONNECT_DATA_FACILITY_AIRPORT from data, skipping
// those without an ident or a position.
func airportsOf(data []byte, n uint32) []position.Airport {
	if n == 0 || uint64(len(data)) < uint64(n)*airportDoubles {
		return nil
	}

	size := elementSize(len(data), int(n))
	identLen := min(size-airportDoubles, maxAirportIdent)

	if identLen <= 0 {
		return nil
	}

	out := make([]position.Airport, 0, n)

	for i := range int(n) {
		el := data[i*size : (i+1)*size]
		pos := el[size-airportDoubles:]

		lat := math.Float64frombits(binary.LittleEndian.Uint64(pos))
		lon := math.Float64frombits(binary.LittleEndian.Uint64(pos[8:]))
		ident := airportIdent(el[:identLen])

		if ident == "" || notFinite(lat) || notFinite(lon) || math.Abs(lat) > 90 ||
			math.Abs(lon) > 180 {
			continue
		}

		out = append(out, position.Airport{Ident: ident, Lat: lat, Lon: lon})
	}

	return out
}

// elementSize is the size of one of n elements in a packet's dataLen
// bytes: MSFS 2024's packed one, unless the packet doesn't fit it.
func elementSize(dataLen, n int) int {
	if dataLen >= airportSize*n && dataLen < airportSize*n+maxTail {
		return airportSize
	}

	return dataLen / n
}

// airportIdent is the NUL-terminated ident, if it is letters and digits.
func airportIdent(raw []byte) string {
	if i := bytes.IndexByte(raw, 0); i >= 0 {
		raw = raw[:i]
	}

	s := strings.TrimSpace(string(raw))
	for _, c := range s {
		if (c < 'A' || c > 'Z') && (c < '0' || c > '9') {
			return ""
		}
	}

	return s
}
