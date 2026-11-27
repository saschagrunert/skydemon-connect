package simconnect

import (
	"io"
	"log"
	"testing"
	"time"

	"github.com/saschagrunert/skydemon-connect/internal/position"
)

// Whatever bytes come from the sim, handling them doesn't panic. A sample
// that isn't finite is passed on: the feed drops it.
func FuzzHandle(f *testing.F) {
	for _, msg := range [][]byte{
		msgOpen(), msgQuit(), msgEvent(eventSim, 1), msgEvent(eventSim, 0),
		msgException(7), goldenData(&testing.T{}),
		msgTraffic(firstAircraft, 1, 1, 1, trafficBlock(&testing.T{}, trafficFloats(), "X")),
		msgTraffic(firstHelicopters, 0, 0, 0, nil),
		{},
		{1, 2, 3},
	} {
		f.Add(msg)
	}

	f.Fuzz(func(_ *testing.T, buf []byte) {
		d := &dispatcher{
			log: log.New(io.Discard, "", 0),
			h: Handlers{
				Update:    func(position.Sample) {},
				Live:      func(bool) {},
				Connected: func(bool) {},
				Ended:     func() {},
				Failed:    func(error) {},
				Traffic:   func([]position.Traffic) {},
			},
		}

		_, _ = d.traffic.due(time.Now(), noAsk)

		// Twice: the second time with whatever state the first left.
		d.handle(buf)
		d.handle(buf)
	})
}
