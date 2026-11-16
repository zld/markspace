// Package ingress feeds decoded packets into the shared buffer controller
// from two sources: reproducible synthetic traces and loopback UDP.
package ingress

import (
	"math"
	"math/rand"
	"sort"
	"time"

	"jitter/internal/wire"
)

// frameInterval is the 20 ms packet cadence (50 packets per second).
const frameInterval = time.Duration(wire.FrameMs) * time.Millisecond

// duplicateDelay is how long after the original a duplicated packet lands.
const duplicateDelay = 3 * time.Millisecond

// Event is one scheduled packet arrival.
type Event struct {
	At  time.Duration
	Pkt wire.Packet
}

// TraceParams describes a reproducible synthetic receive path.
type TraceParams struct {
	Seed      int64
	Frames    int
	Loss      float64 // drop probability per frame
	Reorder   float64 // swap probability with the next slot
	Duplicate float64 // duplicate probability per frame
	JitterMs  float64 // uniform arrival jitter half-width; default 8
}

// PCMForFrame synthesizes the deterministic 500 Hz test tone for a frame.
// 500 Hz has a period of 96 samples, exactly dividing the 960-sample frame,
// so frame boundaries are sample-continuous.
func PCMForFrame(seq uint16) []byte {
	pcm := make([]int16, wire.SamplesPerFrame)
	base := int(seq) * wire.SamplesPerFrame
	for i := range pcm {
		phase := 2 * math.Pi * 500 * float64(base+i) / wire.SampleRate
		pcm[i] = int16(10000 * math.Sin(phase))
	}
	return wire.PCMToBytes(pcm)
}

// GenerateTrace builds a deterministic event list: frames depart every
// 20 ms, land with uniform jitter, and are lost, reordered or duplicated
// according to the configured probabilities.
func GenerateTrace(p TraceParams) []Event {
	if p.JitterMs == 0 {
		p.JitterMs = 8
	}
	rng := rand.New(rand.NewSource(p.Seed))
	var evs []Event
	for i := 0; i < p.Frames; i++ {
		if rng.Float64() < p.Loss {
			continue
		}
		jitter := (rng.Float64()*2 - 1) * p.JitterMs
		at := time.Duration(i)*frameInterval + time.Duration(jitter*float64(time.Millisecond))
		if at < 0 {
			at = 0
		}
		pkt := wire.Packet{
			Seq:       uint16(i),
			Timestamp: uint32(i * wire.SamplesPerFrame),
			Samples:   wire.SamplesPerFrame,
			Payload:   PCMForFrame(uint16(i)),
		}
		evs = append(evs, Event{At: at, Pkt: pkt})
		if rng.Float64() < p.Duplicate {
			evs = append(evs, Event{At: at + duplicateDelay, Pkt: pkt})
		}
	}
	// Reorder: swap payloads of adjacent timing slots, keeping the slots.
	for i := 0; i+1 < len(evs); i++ {
		if rng.Float64() < p.Reorder {
			evs[i].Pkt, evs[i+1].Pkt = evs[i+1].Pkt, evs[i].Pkt
		}
	}
	sort.SliceStable(evs, func(a, b int) bool { return evs[a].At < evs[b].At })
	return evs
}
