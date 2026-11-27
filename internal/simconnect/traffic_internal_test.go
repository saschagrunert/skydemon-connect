package simconnect

import (
	"context"
	"encoding/binary"
	"errors"
	"fmt"
	"log"
	"strings"
	"sync"
	"testing"
	"testing/synctest"
	"time"

	"github.com/saschagrunert/skydemon-connect/internal/position"
	"github.com/saschagrunert/skydemon-connect/internal/testutil"
)

// The request IDs of the first round: aircraft, then helicopters.
const (
	firstAircraft    = firstTrafficRequestID
	firstHelicopters = firstTrafficRequestID + 1
)

// noAsk is a due ask that sends nothing.
func noAsk(uint32, uint32) error { return nil }

// trafficFloats are the floats of a target: 51.5 N 12.25 E at 1000 m,
// 500 fpm up, 120 kt ground speed due east, airborne, not the user.
func trafficFloats() [numTrafficFloats]float64 {
	var v [numTrafficFloats]float64

	v[tLat], v[tLon], v[tAlt] = 51.5, 12.25, 1000
	v[tVS], v[tGS] = 500, 120
	v[tVelEast], v[tHeading] = 200, 80

	return v
}

// trafficBlock is one object's data block.
func trafficBlock(t *testing.T, v [numTrafficFloats]float64, atcID string) []byte {
	t.Helper()

	b, err := binary.Append(nil, binary.LittleEndian, v)
	if err != nil {
		t.Fatal(err)
	}

	var s [string32Len]byte

	copy(s[:], atcID)

	return append(b, s[:]...)
}

// msgTraffic is a SIMCONNECT_RECV_SIMOBJECT_DATA_BYTYPE for entry of
// outOf; data is the object's block, nil for an answer without objects.
func msgTraffic(request, object, entry, outOf uint32, data []byte) []byte {
	defineCount := uint32(0)
	if data != nil {
		defineCount = uint32(len(trafficVars()))
	}

	var b []byte
	for _, v := range []uint32{
		uint32(dataOffset + len(data)), 4, recvSimobjectDataByType,
		request, object, trafficDefineID, 0, entry, outOf, defineCount,
	} {
		b = binary.LittleEndian.AppendUint32(b, v)
	}

	return append(b, data...)
}

func TestToTraffic(t *testing.T) {
	t.Parallel()

	got, ok := toTraffic(trafficBlock(t, trafficFloats(), "D-EABC"))

	want := position.Traffic{
		Callsign: "D-EABC", Lat: 51.5, Lon: 12.25, AltM: 1000, VSFpm: 500,
		TrackTrue: 90, GSKnots: 120, ICAO: 0, Airborne: true,
	}
	if !ok || got != want {
		t.Errorf("got %+v, %v; want %+v", got, ok, want)
	}

	ground := trafficFloats()
	ground[tOnGround] = 1

	if got, _ := toTraffic(trafficBlock(t, ground, "X")); got.Airborne {
		t.Error("on the ground, but airborne")
	}
}

func TestToTrafficSkipped(t *testing.T) {
	t.Parallel()

	user := trafficFloats()
	user[tUser] = 1

	null := trafficFloats()
	null[tLat], null[tLon] = 0, 0

	for name, data := range map[string][]byte{
		"user":        trafficBlock(t, user, "ME"),
		"null island": trafficBlock(t, null, "X"),
		"short":       trafficBlock(t, trafficFloats(), "X")[:trafficDataLen-1],
	} {
		if _, ok := toTraffic(data); ok {
			t.Errorf("%s: not skipped", name)
		}
	}
}

func TestCallsign(t *testing.T) {
	t.Parallel()

	for _, tc := range []struct{ raw, want string }{
		{"DLH4AB", "DLH4AB"},
		{" D-EABC\x00junk", "D-EABC"},
		{"A,B C", "ABC"},
		{"VERYLONGCALLSIGN", "VERYLONG"},
		{"\x00", ""},
		{"é", ""},
	} {
		if got := callsign([]byte(tc.raw)); got != tc.want {
			t.Errorf("callsign(%q) = %q, want %q", tc.raw, got, tc.want)
		}
	}
}

// asks records the requests due sends.
type asks []string

func (a *asks) ask(id, objectType uint32) error {
	*a = append(*a, fmt.Sprintf("%d:%d", id, objectType))

	return nil
}

func TestTrafficState(t *testing.T) {
	t.Parallel()

	synctest.Test(t, func(t *testing.T) {
		var (
			state trafficState
			sent  asks
		)

		a := position.Traffic{Callsign: "A"}
		b := position.Traffic{Callsign: "B"}

		if changed, _ := state.due(time.Now(), sent.ask); changed {
			t.Error("first round changed the traffic")
		}

		// Aircraft: two objects, the second skipped.
		if state.add(firstAircraft, 1, 2, 100, a, true) {
			t.Error("done after entry 1 of 2")
		}

		if !state.add(firstAircraft, 2, 2, 101, b, false) {
			t.Error("not done after entry 2 of 2")
		}

		// Helicopters: none.
		if !state.add(firstHelicopters, 0, 0, 0, position.Traffic{}, false) {
			t.Error("an empty answer is not done")
		}

		if got := state.all(); len(got) != 1 || got[0].Callsign != "A" || got[0].ICAO != 1 {
			t.Errorf("all %+v", got)
		}

		// A late duplicate is ignored.
		if state.add(firstAircraft, 2, 2, 101, b, true) {
			t.Error("added to a finished answer")
		}

		// Nothing is asked for again before trafficInterval.
		_, _ = state.due(time.Now().Add(trafficInterval-time.Millisecond), sent.ask)

		if want := "2:2 3:3"; strings.Join(sent, " ") != want {
			t.Errorf("asked %q, want %q", sent, want)
		}
	})
}

// A sim that stalls keeps its traffic until trafficTimeout, and the late
// answer to a request asked for again is not taken for the new one's.
func TestTrafficStalls(t *testing.T) {
	t.Parallel()

	var (
		state trafficState
		sent  asks
	)

	start := time.Now()
	a := position.Traffic{Callsign: "A"}

	_, _ = state.due(start, sent.ask)
	state.add(firstAircraft, 1, 1, 100, a, true)
	state.add(firstHelicopters, 0, 0, 0, position.Traffic{}, false)

	// Round 2 gets no answer: the traffic stays, nothing new is asked.
	_, _ = state.due(start.Add(trafficInterval), sent.ask)

	for _, at := range []time.Duration{2 * trafficInterval, trafficTimeout} {
		if changed, _ := state.due(start.Add(at), sent.ask); changed || len(state.all()) != 1 {
			t.Errorf("%v: changed %v, traffic %+v", at, changed, state.all())
		}
	}

	// Past trafficTimeout it is dropped and asked for again.
	if changed, _ := state.due(start.Add(trafficInterval+trafficTimeout), sent.ask); !changed ||
		len(state.all()) != 0 {
		t.Errorf("after the timeout: changed %v, traffic %+v", changed, state.all())
	}

	if want := "2:2 3:3 4:2 5:3 6:2 7:3"; strings.Join(sent, " ") != want {
		t.Errorf("asked %q, want %q", sent, want)
	}

	// Round 2's late answer is ignored, round 3's taken.
	if state.add(4, 1, 1, 100, a, true) {
		t.Error("late answer taken")
	}

	if !state.add(6, 1, 1, 100, a, true) || len(state.all()) != 1 {
		t.Errorf("current answer: %+v", state.all())
	}
}

// ICAO addresses are unique per object, the same for the same object, and
// never 0; the label of a target without an ATC ID is made from it.
func TestTrafficAddresses(t *testing.T) {
	t.Parallel()

	var state trafficState

	_, _ = state.due(time.Now(), noAsk)

	// Low 24 bits 0 and equal: the object IDs' own would be 0 and clash.
	ids := []uint32{1 << 24, 2 << 24, 1 << 24}
	for i, id := range ids {
		state.add(firstAircraft, uint32(i+1), uint32(len(ids)), id, position.Traffic{}, true)
	}

	got := state.all()
	if len(got) != 3 || got[0].ICAO != 1 || got[1].ICAO != 2 || got[2].ICAO != 1 {
		t.Fatalf("addresses %+v", got)
	}

	if got[0].Callsign != "T000001" || got[1].Callsign != "T000002" {
		t.Errorf("labels %q %q", got[0].Callsign, got[1].Callsign)
	}

	state.lastICAO = maxICAO
	if a := state.address(3 << 24); a != 1 {
		t.Errorf("after the last address: %d, want 1", a)
	}
}

// trafficRecorder records the handler calls, with the Traffic calls as
// "traffic=<callsigns>".
type trafficRecorder struct {
	recorder

	mu   sync.Mutex
	asks []string
}

func (r *trafficRecorder) handlers() Handlers {
	h := r.recorder.handlers()
	h.Traffic = func(t []position.Traffic) {
		cs := make([]string, 0, len(t))
		for _, x := range t {
			cs = append(cs, x.Callsign)
		}

		r.add("traffic=" + strings.Join(cs, " "))
	}

	return h
}

func (r *trafficRecorder) ask(request, objectType, radius uint32) {
	r.mu.Lock()
	r.asks = append(r.asks, fmt.Sprintf("%d:%d:%d", request, objectType, radius))
	r.mu.Unlock()
}

func TestLoopTraffic(t *testing.T) {
	t.Parallel()

	synctest.Test(t, func(t *testing.T) {
		ctx, cancel := context.WithTimeout(t.Context(), time.Hour)
		defer cancel()

		var r trafficRecorder

		own := trafficFloats()
		own[tUser] = 1

		c := &fakeConn{msgs: [][]byte{msgOpen()}}
		c.onTraffic = func(c *fakeConn, request, objectType, radius uint32) {
			r.ask(request, objectType, radius)

			switch objectType {
			case objectTypeAircraft:
				c.msgs = append(c.msgs,
					msgTraffic(request, 7, 1, 2, trafficBlock(t, own, "ME")),
					msgTraffic(request, 8, 2, 2, trafficBlock(t, trafficFloats(), "DLH4AB")))
			case objectTypeHelicopter:
				c.msgs = append(c.msgs, msgTraffic(request, 0, 0, 0, nil), msgQuit())
			}
		}

		loop(ctx, testutil.Log(t), opensOnce(c, cancel), r.handlers())

		if want := "traffic=DLH4AB,traffic=DLH4AB,traffic=,ended"; r.String() != want {
			t.Errorf("events %q, want %q", r.String(), want)
		}

		if want := "2:2:55560 3:3:55560"; strings.Join(r.asks, " ") != want {
			t.Errorf("asked %q, want %q", r.asks, want)
		}
	})
}

// A traffic request that fails ends the session's traffic, not the
// session: the positions go on, and traffic isn't asked for again.
func TestSessionTrafficRequestFails(t *testing.T) {
	t.Parallel()

	synctest.Test(t, func(t *testing.T) {
		var (
			r   trafficRecorder
			out testutil.Buffer
		)

		c := &fakeConn{
			requestErr: errFake,
			msgs:       [][]byte{msgOpen(), msgEvent(eventSim, 1)},
		}
		c.onTraffic = func(c *fakeConn, request, objectType, radius uint32) {
			r.ask(request, objectType, radius)

			// Later frames still come: the session goes on.
			c.msgs = append(c.msgs, goldenData(t), msgQuit())
		}

		connected, err := session(t.Context(), log.New(&out, "", 0),
			func(string) (conn, error) { return c, nil }, r.handlers())
		if !connected || !errors.Is(err, errQuit) {
			t.Errorf("connected %v, err %v", connected, err)
		}

		want := "simconnect: no traffic: request traffic: fake"
		if !strings.Contains(out.String(), want) {
			t.Errorf("log %q, want %q", out.String(), want)
		}

		if want = "traffic=,live,update"; r.String() != want {
			t.Errorf("events %q, want %q", r.String(), want)
		}

		if len(r.asks) != 1 {
			t.Errorf("asked %q, want once", r.asks)
		}
	})
}

// Traffic that can't be defined leaves the positions: the session goes on
// without asking for traffic.
func TestLoopTrafficDefineFails(t *testing.T) {
	t.Parallel()

	synctest.Test(t, func(t *testing.T) {
		ctx, cancel := context.WithTimeout(t.Context(), time.Hour)
		defer cancel()

		var (
			r   trafficRecorder
			out testutil.Buffer
		)

		c := &fakeConn{
			trafficErr: errFake,
			msgs:       [][]byte{msgOpen(), msgEvent(eventSim, 1), goldenData(t), msgQuit()},
			onTraffic: func(*fakeConn, uint32, uint32, uint32) {
				t.Error("traffic asked for")
			},
		}

		loop(ctx, log.New(&out, "", 0), opensOnce(c, cancel), r.handlers())

		want := "simconnect: no traffic: add PLANE LATITUDE: fake"
		if !strings.Contains(out.String(), want) {
			t.Errorf("log %q, want %q", out.String(), want)
		}

		if want = "live,update,dead,ended"; r.String() != want {
			t.Errorf("events %q, want %q", r.String(), want)
		}
	})
}

// opensOnce opens c, then cancels: the loop ends after c's session.
func opensOnce(c *fakeConn, cancel func()) opener {
	n := 0

	return func(string) (conn, error) {
		n++
		if n > 1 {
			cancel()

			return nil, errFake
		}

		return c, nil
	}
}

// A block shorter than the traffic definition, as after a variable the sim
// rejected, is logged once and its target dropped.
func TestTrafficDataShortLogged(t *testing.T) {
	t.Parallel()

	var (
		r   trafficRecorder
		out testutil.Buffer
	)

	d := &dispatcher{log: log.New(&out, "", 0), h: r.handlers()}
	_, _ = d.traffic.due(time.Now(), noAsk)

	short := trafficBlock(t, trafficFloats(), "X")[:trafficDataLen-string32Len]
	d.handle(msgTraffic(firstAircraft, 8, 1, 2, short))
	d.handle(msgTraffic(firstAircraft, 9, 2, 2, short))

	want := fmt.Sprintf("simconnect: traffic data block of %d bytes (11 variables), want %d bytes "+
		"(11 variables), not used", trafficDataLen-string32Len, trafficDataLen)
	if n := strings.Count(out.String(), want); n != 1 {
		t.Errorf("logged %d times, want once: %q", n, out.String())
	}

	if want = "traffic="; r.String() != want {
		t.Errorf("events %q, want %q", r.String(), want)
	}
}
