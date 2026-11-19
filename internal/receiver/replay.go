// Package receiver drives the shared jitter controller from two sources:
// deterministic offline event streams (Replay) and live UDP datagrams
// (Listen). Both paths decode packets identically and never log payloads.
package receiver

import (
	"time"

	"jitterlab/internal/jitter"
	"jitterlab/internal/protocol"
	"jitterlab/internal/scenario"
)

// Replay feeds an offline event stream into ctl in event-time order,
// advancing playout between arrivals. tail extends playout past the last
// arrival so trailing gaps are concealed before the final drain.
func Replay(events []scenario.Event, ctl *jitter.Controller, maxPayload int, tail time.Duration) {
	var last time.Duration
	for _, e := range events {
		if e.At > last {
			last = e.At
		}
		ctl.PlayoutUntil(e.At)
		pkt, err := protocol.Decode(e.Data, maxPayload)
		if err != nil {
			ctl.M.Malformed++
			continue
		}
		ctl.AddPacket(pkt, e.At)
	}
	ctl.PlayoutUntil(last + tail)
	ctl.Drain()
}
