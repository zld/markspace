// Package ingress feeds decoded packets from the two input paths (UDP
// loopback and generated trace replay) into the shared jitter controller.
package ingress

import (
	"jbcheck/internal/jitter"
	"jbcheck/internal/metrics"
	"jbcheck/internal/packet"
	"jbcheck/internal/sim"
)

// RunTrace replays events on a virtual clock through the shared controller.
// Corrupt events are counted, not fatal; a trace that ends early simply
// stops feeding the controller, which is recorded via EndReason.
func RunTrace(events []sim.Event, ctrl *jitter.Controller, st *metrics.Stats) {
	var last int64
	for _, ev := range events {
		ctrl.Tick(ev.ArrivalMs)
		last = ev.ArrivalMs
		p, err := packet.Decode(ev.Data)
		if err != nil {
			st.BadPackets++
			continue
		}
		pcm, err := packet.DecodePCM(p.Payload)
		if err != nil {
			st.BadPackets++
			continue
		}
		ctrl.Add(p, pcm, ev.ArrivalMs)
	}
	// Drain: play out what is still buffered, but do not pad the tail
	// with concealment frames for packets the trace never contained.
	for t := last; ctrl.QueueLen() > 0 && t < last+2000; t += 10 {
		ctrl.Tick(t)
	}
	if len(events) == 0 {
		st.EndReason = "trace_empty"
	} else {
		st.EndReason = "trace_complete"
	}
}
