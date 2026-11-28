package simconnect

import (
	"bytes"
	"encoding/binary"
	"math"
	"strings"

	"github.com/saschagrunert/skydemon-connect/internal/position"
)

// The airports of the whole world, asked for once a session with
// SimConnect_RequestAllFacilities: about 40,000, in packets of
// SIMCONNECT_RECV_AIRPORT_LIST.
const (
	recvAirportList = 18 // SIMCONNECT_RECV_ID_AIRPORT_LIST
	// airportsRequestID is far from the traffic requests' IDs, which
	// count up.
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
)

// airportState collects the packets of the airport list.
type airportState struct {
	packets map[uint32][]position.Airport
	// outOf is the packet count, done set once all came.
	outOf uint32
	done  bool
}

// listHeader is the SIMCONNECT_RECV_FACILITIES_LIST part of a packet: the
// array size, the packet's number and the packet count.
type listHeader struct{ n, entry, outOf uint32 }

// readListHeader reads the header of a packet of the airport request; ok
// is false for another request's or a broken one.
func readListHeader(buf []byte) (listHeader, bool) {
	if len(buf) < listDataOffset ||
		binary.LittleEndian.Uint32(buf[listRequestOffset:]) != airportsRequestID {
		return listHeader{n: 0, entry: 0, outOf: 0}, false
	}

	h := listHeader{
		n:     binary.LittleEndian.Uint32(buf[listSizeOffset:]),
		entry: binary.LittleEndian.Uint32(buf[listEntryOffset:]),
		outOf: binary.LittleEndian.Uint32(buf[listOutOfOffset:]),
	}

	return h, h.outOf > 0 && h.outOf <= maxAirportPackets && h.entry < h.outOf
}

// add takes one SIMCONNECT_RECV_AIRPORT_LIST and returns the whole list
// once its last packet came, else nil.
func (as *airportState) add(buf []byte) []position.Airport {
	h, ok := readListHeader(buf)
	if as.done || !ok {
		return nil
	}

	if as.packets == nil {
		as.packets, as.outOf = map[uint32][]position.Airport{}, h.outOf
	}

	if h.outOf != as.outOf {
		return nil // not this list's
	}

	as.packets[h.entry] = airportsOf(buf[listDataOffset:], h.n)

	if len(as.packets) < int(as.outOf) {
		return nil
	}

	as.done = true

	var all []position.Airport

	for i := range as.outOf {
		all = append(all, as.packets[i]...)
	}

	as.packets = nil

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
