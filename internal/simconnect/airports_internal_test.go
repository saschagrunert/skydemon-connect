package simconnect

import (
	"context"
	"encoding/binary"
	"log"
	"math"
	"strings"
	"testing"
	"testing/synctest"
	"time"

	"github.com/saschagrunert/skydemon-connect/internal/position"
	"github.com/saschagrunert/skydemon-connect/internal/testutil"
)

// msgAirports is a SIMCONNECT_RECV_AIRPORT_LIST, packet entry of outOf, with
// MSFS 2024's SIMCONNECT_DATA_FACILITY_AIRPORT of identLen bytes for the
// ident and region, packed.
func msgAirports(request, entry, outOf uint32, identLen int, airports ...position.Airport) []byte {
	var data []byte

	for _, a := range airports {
		ident := make([]byte, identLen)
		copy(ident, a.Ident)

		data = append(data, ident...)
		data = binary.LittleEndian.AppendUint64(data, math.Float64bits(a.Lat))
		data = binary.LittleEndian.AppendUint64(data, math.Float64bits(a.Lon))
		data = binary.LittleEndian.AppendUint64(data, math.Float64bits(100))
	}

	var b []byte
	for _, v := range []uint32{
		uint32(listDataOffset + len(data)), 4, recvAirportList,
		request, uint32(len(airports)), entry, outOf,
	} {
		b = binary.LittleEndian.AppendUint32(b, v)
	}

	return append(b, data...)
}

// testAirports are EDDP, EDAD and LIPH.
func testAirports() (position.Airport, position.Airport, position.Airport) {
	return position.Airport{Ident: "EDDP", Lat: 51.4239, Lon: 12.2364},
		position.Airport{Ident: "EDAD", Lat: 51.8313, Lon: 12.1861},
		position.Airport{Ident: "LIPH", Lat: 45.6484, Lon: 12.1944}
}

func TestAirportList(t *testing.T) {
	t.Parallel()

	eddp, edad, liph := testAirports()

	var state airportState

	// Before a request, nothing is taken.
	if got := state.add(msgAirports(airportsRequestID, 0, 1, 12, liph)); got != nil {
		t.Fatalf("taken unasked: %+v", got)
	}

	state.ask(time.Now())

	// The packets in any order; others' and broken ones are ignored.
	for _, msg := range [][]byte{
		msgAirports(airportsRequestID+1, 0, 2, 12, liph),
		msgAirports(airportsRequestID, 3, 2, 12, liph),
		msgAirports(airportsRequestID, 0, 0, 12, liph),
		msgAirports(airportsRequestID, 0, 2, 12, eddp)[:listDataOffset-1],
		msgAirports(airportsRequestID, 1, 2, 12, liph),
	} {
		if got := state.add(msg); got != nil {
			t.Fatalf("list complete too early: %+v", got)
		}
	}

	got := state.add(msgAirports(airportsRequestID, 0, 2, 12, eddp, edad))
	if len(got) != 3 || got[0] != eddp || got[1] != edad || got[2] != liph {
		t.Errorf("airports %+v", got)
	}

	// Once complete, a late duplicate changes nothing.
	if again := state.add(msgAirports(airportsRequestID, 1, 2, 12, liph)); again != nil {
		t.Errorf("after the list %+v", again)
	}
}

// A packet of another list length than the first is not this list's.
func TestAirportListOtherOutOf(t *testing.T) {
	t.Parallel()

	eddp, _, liph := testAirports()

	var state airportState

	state.ask(time.Now())
	state.add(msgAirports(airportsRequestID, 0, 2, 12, eddp))

	if got := state.add(msgAirports(airportsRequestID, 0, 1, 12, liph)); got != nil {
		t.Errorf("mixed lists %+v", got)
	}
}

func TestAirportsOf(t *testing.T) {
	t.Parallel()

	eddp, _, liph := testAirports()

	// Padded to 16 bytes before the doubles, as an unpacked struct is.
	padded := msgAirports(airportsRequestID, 0, 1, 16, eddp, liph)

	got := airportsOf(padded[listDataOffset:], 2)
	if len(got) != 2 || got[0] != eddp || got[1] != liph {
		t.Errorf("padded %+v", got)
	}

	// One airport with padding after it: still at its place.
	tail := append(msgAirports(airportsRequestID, 0, 1, 12, eddp), 0, 0, 0, 0)

	got = airportsOf(tail[listDataOffset:], 1)
	if len(got) != 1 || got[0] != eddp {
		t.Errorf("with a tail %+v", got)
	}

	bad := msgAirports(airportsRequestID, 0, 1, 12,
		position.Airport{Ident: "", Lat: 1, Lon: 1},
		position.Airport{Ident: "edxx", Lat: 1, Lon: 1},
		position.Airport{Ident: "EDXX", Lat: math.NaN(), Lon: 1},
		position.Airport{Ident: "EDYY", Lat: 91, Lon: 1},
		position.Airport{Ident: "EDZZ", Lat: 1, Lon: 181},
		position.Airport{Ident: "EDNY", Lat: 47.6713, Lon: 9.5114},
	)

	if got := airportsOf(bad[listDataOffset:], 6); len(got) != 1 || got[0].Ident != "EDNY" {
		t.Errorf("bad ones kept: %+v", got)
	}

	if airportsOf(nil, 0) != nil || airportsOf(make([]byte, 10), 1) != nil ||
		airportsOf(make([]byte, 24), 1) != nil {
		t.Error("no room for an airport, but got one")
	}
}

func TestLoopAirports(t *testing.T) {
	t.Parallel()

	eddp, _, liph := testAirports()

	synctest.Test(t, func(t *testing.T) {
		ctx, cancel := context.WithTimeout(t.Context(), time.Hour)
		defer cancel()

		var (
			r   recorder
			got []position.Airport
			out testutil.Buffer
		)

		c := &fakeConn{msgs: [][]byte{msgOpen()}}
		c.onAirports = func(c *fakeConn, request uint32) error {
			c.msgs = append(c.msgs,
				msgAirports(request, 1, 2, 12, liph),
				msgAirports(request, 0, 2, 12, eddp),
				msgQuit())

			return nil
		}

		h := r.handlers()
		h.Airports = func(a []position.Airport) { got = a }

		loop(ctx, log.New(&out, "", 0), opensOnce(c, cancel), h)

		if len(got) != 2 || got[0] != eddp || got[1] != liph {
			t.Errorf("airports %+v", got)
		}

		if !strings.Contains(out.String(), "simconnect: 2 airports") {
			t.Errorf("log:\n%s", out.String())
		}
	})
}

// Without the airports, e.g. from an older SimConnect.dll, the positions
// go on.
func TestLoopAirportsFail(t *testing.T) {
	t.Parallel()

	synctest.Test(t, func(t *testing.T) {
		ctx, cancel := context.WithTimeout(t.Context(), time.Hour)
		defer cancel()

		var (
			r   recorder
			out testutil.Buffer
		)

		c := &fakeConn{
			msgs:       [][]byte{msgOpen(), msgEvent(eventSim, 1), goldenData(t), msgQuit()},
			onAirports: func(*fakeConn, uint32) error { return errFake },
		}

		h := r.handlers()
		h.Airports = func([]position.Airport) { t.Error("airports without asking") }

		loop(ctx, log.New(&out, "", 0), opensOnce(c, cancel), h)

		if !strings.Contains(out.String(), "simconnect: no airports: fake") ||
			!strings.HasPrefix(r.String(), "live,update") {
			t.Errorf("events %q, log:\n%s", r.String(), out.String())
		}
	})
}

// Asked in the main menu, a short list is asked for again once a flight
// runs, not before; a whole one isn't.
func TestLoopAirportsAgain(t *testing.T) {
	t.Parallel()

	eddp, _, liph := testAirports()

	synctest.Test(t, func(t *testing.T) {
		ctx, cancel := context.WithTimeout(t.Context(), time.Hour)
		defer cancel()

		var (
			r     recorder
			lists []int
			// atLive is how often the airports were asked for at each
			// flight start.
			atLive []int
		)

		asked := 0
		c := &fakeConn{msgs: [][]byte{msgOpen(), msgEvent(eventSim, 0)}}
		c.onAirports = func(c *fakeConn, request uint32) error {
			asked++

			switch asked {
			case 1: // the menu's: short, then a while in the menu
				c.msgs = append(c.msgs, msgAirports(request, 0, 1, 12, eddp),
					msgEvent(eventSim, 0), msgEvent(eventSim, 0), msgEvent(eventSim, 1))
			default:
				many := make([]position.Airport, minAirports)
				for i := range many {
					many[i] = liph
				}

				c.msgs = append(c.msgs, msgAirports(request, 0, 1, 12, many...),
					msgEvent(eventSim, 0), msgEvent(eventSim, 1), msgQuit())
			}

			return nil
		}

		h := r.handlers()
		live := h.Live
		h.Live = func(running bool) {
			if running {
				atLive = append(atLive, asked)
			}

			live(running)
		}
		h.Airports = func(a []position.Airport) { lists = append(lists, len(a)) }

		loop(ctx, testutil.Log(t), opensOnce(c, cancel), h)

		if asked != 2 || len(lists) != 2 || lists[0] != 1 || lists[1] != minAirports ||
			len(atLive) != 2 || atLive[0] != 1 || atLive[1] != 2 {
			t.Errorf("asked %d times, %v at the flight starts, lists %v", asked, atLive, lists)
		}
	})
}

// A request without an answer is asked again at a flight start once it
// counts as lost.
func TestAirportsLost(t *testing.T) {
	t.Parallel()

	var state airportState

	now := time.Now()
	state.ask(now)

	state.again(now.Add(time.Second))

	if !state.asked {
		t.Error("asked again while the answer may come")
	}

	state.again(now.Add(airportsLost))

	if state.asked {
		t.Error("a lost request not asked again")
	}

	// Without a flight start, a few times; each request has its own ID,
	// so that a late answer to a lost one isn't taken for the new one's.
	for i := range maxLostAirports + 1 {
		id := state.nextID()
		state.ask(now)
		state.retryLost(now.Add(airportsLost))

		if state.asked != (i == maxLostAirports) {
			t.Errorf("try %d: asked %v", i, state.asked)
		}

		if i > 0 && id == airportsRequestID {
			t.Error("the same ID again")
		}
	}

	_, eddp, _ := testAirports()
	if state.add(msgAirports(airportsRequestID, 0, 1, 12, eddp)) != nil {
		t.Error("an earlier request's answer taken")
	}
}
