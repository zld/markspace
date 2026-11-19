// Package scenario synthesizes deterministic offline arrival events:
// loss bursts, bounded reordering, duplicates, jitter and clock drift.
// A fixed seed fully determines the generated stream.
package scenario

import (
	"math/rand"
	"sort"
	"time"

	"jitterlab/internal/protocol"
)

// Config describes one synthetic network path.
type Config struct {
	Frames        int
	Seed          int64
	StreamID      uint32
	LossBurst     int     // max packets dropped per burst
	ReorderDepth  int     // max packets held back for reordering
	DuplicateRate float64 // probability a packet is sent twice
	JitterMs      float64 // stddev of gaussian arrival jitter
	DriftPPM      float64 // sender clock drift, parts per million
	FrameInterval time.Duration
	PayloadBytes  int
}

// Event is one packet arrival in offline event time.
type Event struct {
	At   time.Duration // arrival time relative to replay start
	Data []byte        // encoded datagram
}

// Generate builds the event stream. Events are sorted by arrival time.
func Generate(cfg Config) []Event {
	if cfg.Frames <= 0 {
		return nil
	}
	if cfg.FrameInterval <= 0 {
		cfg.FrameInterval = 20 * time.Millisecond
	}
	if cfg.PayloadBytes <= 0 {
		cfg.PayloadBytes = 160
	}
	rnd := rand.New(rand.NewSource(cfg.Seed))
	n := cfg.Frames
	intervalMs := float64(cfg.FrameInterval) / float64(time.Millisecond)

	lost := lossPattern(rnd, n, cfg.LossBurst)
	order := emissionOrder(rnd, n, lost, cfg.ReorderDepth)

	// Per-packet arrival on the receiver clock: sender spacing scaled by
	// drift, plus a base delay and gaussian jitter.
	baseDelayMs := 40 + 3*cfg.JitterMs
	drift := cfg.DriftPPM / 1e6
	arrival := make([]float64, n)
	for i := 0; i < n; i++ {
		a := float64(i)*intervalMs*(1+drift) + baseDelayMs + rnd.NormFloat64()*cfg.JitterMs
		if a < 0 {
			a = 0
		}
		arrival[i] = a
	}

	var evs []Event
	last := 0.0
	for _, idx := range order {
		a := arrival[idx]
		if a < last {
			a = last // arrivals stay nondecreasing in emission order
		}
		last = a
		data := protocol.Encode(protocol.Packet{
			StreamID:   cfg.StreamID,
			Seq:        uint32(idx),
			SendTimeMs: 1_000_000 + int64(float64(idx)*intervalMs),
			Type:       protocol.FrameAudio,
			Payload:    payload(cfg.PayloadBytes, idx),
		})
		evs = append(evs, Event{At: msToDur(a), Data: data})
		if rnd.Float64() < cfg.DuplicateRate {
			dup := Event{At: msToDur(a + 1 + rnd.Float64()*5), Data: append([]byte(nil), data...)}
			evs = append(evs, dup)
		}
	}
	sort.SliceStable(evs, func(i, j int) bool { return evs[i].At < evs[j].At })
	return evs
}

// lossPattern marks burst losses; guarantees at least one burst so
// concealment is exercised whenever LossBurst > 0.
func lossPattern(rnd *rand.Rand, n, burst int) []bool {
	lost := make([]bool, n)
	if burst <= 0 {
		return lost
	}
	p := 3.0 / float64(n) // a handful of bursts per stream on average
	for i := 0; i < n; {
		if rnd.Float64() < p {
			l := 1 + rnd.Intn(burst)
			for k := 0; k < l && i < n; k++ {
				lost[i] = true
				i++
			}
		} else {
			i++
		}
	}
	any := false
	for _, l := range lost {
		any = any || l
	}
	if !any {
		for k := 0; k < burst && n/2+k < n; k++ {
			lost[n/2+k] = true
		}
	}
	return lost
}

// emissionOrder emits surviving seqs with per-packet displacement bounded
// by depth positions, so reordering never exceeds the configured window.
// depth 0 keeps natural order.
func emissionOrder(rnd *rand.Rand, n int, lost []bool, depth int) []int {
	type displaced struct {
		pos, seq int
	}
	var items []displaced
	for i := 0; i < n; i++ {
		if lost[i] {
			continue
		}
		d := 0
		if depth > 0 {
			d = rnd.Intn(depth + 1) // hold back at most depth positions
		}
		items = append(items, displaced{pos: i + d, seq: i})
	}
	sort.Slice(items, func(a, b int) bool {
		if items[a].pos != items[b].pos {
			return items[a].pos < items[b].pos
		}
		return items[a].seq < items[b].seq
	})
	order := make([]int, len(items))
	for i, it := range items {
		order[i] = it.seq
	}
	return order
}

// payload derives deterministic pseudo-audio bytes from the seq number.
func payload(size, seq int) []byte {
	b := make([]byte, size)
	v := uint32(seq)*2654435761 + 97
	for i := range b {
		v ^= v << 13
		v ^= v >> 17
		v ^= v << 5
		b[i] = byte(v)
	}
	return b
}

func msToDur(ms float64) time.Duration {
	return time.Duration(ms * float64(time.Millisecond))
}
