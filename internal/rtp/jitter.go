// Package rtp implements a reordering jitter buffer and packet-loss recovery
// for RTP return-link captures. It shares the timeline nanosecond clock and
// the unified error taxonomy with the other subsystems.
package rtp

import (
	"sync"

	"github.com/pion/rtp"

	"mediaarchive/internal/timeline"
)

// RecoveryPolicy decides how a missing packet inside the jitter window is
// handled once the window slides past it.
type RecoveryPolicy int

const (
	// PolicyGap leaves an explicit unrecovered gap and counts it.
	PolicyGap RecoveryPolicy = iota
	// PolicyPLC marks the slot as recovered via packet-loss concealment.
	// No payload is synthesized; the slot is recorded as recovered.
	PolicyPLC
	// PolicyRepeat reuses the most recent in-order packet as a placeholder.
	PolicyRepeat
)

func (p RecoveryPolicy) String() string {
	switch p {
	case PolicyGap:
		return "gap"
	case PolicyPLC:
		return "plc"
	case PolicyRepeat:
		return "repeat"
	default:
		return "unknown"
	}
}

// Config sizes the buffer. Window is the reorder tolerance in sequence
// numbers; MaxBuffered bounds memory regardless of packet size.
type Config struct {
	Window      uint16
	Policy      RecoveryPolicy
	MaxBuffered int
}

// DefaultConfig returns a 32-packet window with explicit gap marking.
func DefaultConfig() Config {
	return Config{Window: 32, Policy: PolicyGap, MaxBuffered: 4096}
}

// SlotClass tells the merger how a delivered slot was produced.
type SlotClass int

const (
	SlotReceived SlotClass = iota
	SlotReordered
	SlotDuplicate
	SlotRecovered
	SlotGap
)

func (c SlotClass) String() string {
	switch c {
	case SlotReceived:
		return "received"
	case SlotReordered:
		return "reordered"
	case SlotDuplicate:
		return "duplicate"
	case SlotRecovered:
		return "recovered"
	default:
		return "gap"
	}
}

// Slot is one output position in sequence-number order.
type Slot struct {
	Sequence  uint64 // unwrapped sequence number
	Class     SlotClass
	Packet    *rtp.Packet // nil for recovered/gap placeholders
	Arrival   int64       // arrival order index
	Recovered bool
}

// Stats accumulates buffer counters for the archive quality summary.
type Stats struct {
	Received           int
	Duplicates         int
	Reordered          int
	RecoveredPackets   int
	UnrecoveredPackets int
	WindowGaps         int
	MaxHeld            int
}

// entry is an internally buffered packet with arrival metadata.
type entry struct {
	pkt       *rtp.Packet
	seq       uint64
	arrival   int64
	reordered bool
}

// JitterBuffer reorders a stream with 16-bit sequence-number wraparound.
type JitterBuffer struct {
	mu        sync.Mutex
	cfg       Config
	clock     *timeline.WrapClock
	held      map[uint64]entry
	nextOut   uint64 // next sequence number to emit
	highWater uint64 // highest sequence number ever observed
	arrival   int64
	started   bool
	last      *rtp.Packet // most recent in-order packet, for PolicyRepeat
	stats     Stats
}

// NewJitterBuffer constructs a buffer from config.
func NewJitterBuffer(cfg Config) *JitterBuffer {
	if cfg.Window == 0 {
		cfg.Window = 32
	}
	if cfg.MaxBuffered == 0 {
		cfg.MaxBuffered = 4096
	}
	return &JitterBuffer{
		cfg:   cfg,
		clock: timeline.NewWrapClock(16),
		held:  make(map[uint64]entry),
	}
}

// Push accepts one packet in arrival order. A packet whose sequence number
// has already been emitted, or a second copy of a buffered sequence, is a
// duplicate and counted rather than re-delivered.
func (b *JitterBuffer) Push(pkt *rtp.Packet) error {
	if pkt == nil {
		return timeline.NewError("rtp", "push", -1, timeline.RecoverNone,
			"nil packet", timeline.ErrOutOfBounds)
	}
	b.mu.Lock()
	defer b.mu.Unlock()

	b.arrival++
	seq := b.clock.Unwrap(uint64(pkt.SequenceNumber))
	b.stats.Received++

	if !b.started {
		b.started = true
		b.nextOut = seq
		b.highWater = seq
	}
	// Already delivered: duplicate.
	if seq < b.nextOut {
		b.stats.Duplicates++
		return nil
	}
	if _, exists := b.held[seq]; exists {
		b.stats.Duplicates++
		return nil
	}
	if len(b.held) >= b.cfg.MaxBuffered {
		return timeline.NewError("rtp", "push", int64(seq), timeline.RecoverNone,
			"jitter buffer capacity exceeded", timeline.ErrLimitExceeded)
	}
	// A packet whose sequence sits below the observed high-water mark but
	// has not been emitted yet arrived out of order (a late, valid packet).
	reordered := b.started && seq < b.highWater
	if reordered {
		b.stats.Reordered++
	}
	if seq > b.highWater {
		b.highWater = seq
	}
	b.held[seq] = entry{pkt: pkt, seq: seq, arrival: b.arrival, reordered: reordered}
	if len(b.held) > b.stats.MaxHeld {
		b.stats.MaxHeld = len(b.held)
	}
	return nil
}

// Pop emits every slot that can now be resolved: contiguous received
// packets, plus placeholders for missing slots once the window slides past
// them. Output is always in unwrapped sequence-number order.
func (b *JitterBuffer) Pop() []Slot {
	b.mu.Lock()
	defer b.mu.Unlock()

	var out []Slot
	for {
		if e, ok := b.held[b.nextOut]; ok {
			class := SlotReceived
			if e.reordered {
				class = SlotReordered
			}
			out = append(out, Slot{
				Sequence: e.seq, Class: class,
				Packet: e.pkt, Arrival: e.arrival,
			})
			delete(b.held, e.seq)
			b.last = e.pkt
			b.nextOut++
			continue
		}
		// No packet at the frontier. Decide whether the window has passed.
		if b.highestLocked() >= b.nextOut+uint64(b.cfg.Window) {
			out = append(out, b.resolveGap(b.nextOut))
			b.nextOut++
			continue
		}
		break
	}
	return out
}

// highestLocked reports the largest buffered sequence number.
func (b *JitterBuffer) highestLocked() uint64 {
	var hi uint64
	for s := range b.held {
		if s > hi {
			hi = s
		}
	}
	return hi
}

// resolveGap applies the configured policy to a missing frontier slot.
func (b *JitterBuffer) resolveGap(seq uint64) Slot {
	b.stats.WindowGaps++
	switch b.cfg.Policy {
	case PolicyPLC, PolicyRepeat:
		b.stats.RecoveredPackets++
		s := Slot{Sequence: seq, Class: SlotRecovered, Recovered: true}
		if b.cfg.Policy == PolicyRepeat && b.last != nil {
			s.Packet = b.last
		}
		return s
	default:
		b.stats.UnrecoveredPackets++
		return Slot{Sequence: seq, Class: SlotGap}
	}
}

// Flush drains the buffer at stream end. Missing slots at or below the
// highest sequence ever observed are resolved through the configured policy
// (they lived inside the jitter window). Terminal losses above the observed
// high-water mark are not emitted here; callers use FlushTerminal to record
// the count of packets that never arrived before the stream ended.
func (b *JitterBuffer) Flush() []Slot {
	b.mu.Lock()
	defer b.mu.Unlock()

	var out []Slot
	top := b.highestLocked()
	for b.nextOut <= top {
		if e, ok := b.held[b.nextOut]; ok {
			out = append(out, Slot{
				Sequence: e.seq, Class: SlotReceived,
				Packet: e.pkt, Arrival: e.arrival,
			})
			delete(b.held, e.seq)
			b.last = e.pkt
		} else {
			out = append(out, b.resolveGap(b.nextOut))
		}
		b.nextOut++
	}
	return out
}

// FlushTerminal records count unrecovered gaps immediately following the
// observed high-water mark and returns their placeholder slots.
func (b *JitterBuffer) FlushTerminal(count int) []Slot {
	b.mu.Lock()
	defer b.mu.Unlock()

	out := make([]Slot, 0, count)
	for i := 0; i < count; i++ {
		out = append(out, b.resolveTerminalGap(b.nextOut))
		b.nextOut++
	}
	return out
}

// resolveTerminalGap records an end-of-stream loss that the window could
// never have concealed.
func (b *JitterBuffer) resolveTerminalGap(seq uint64) Slot {
	b.stats.WindowGaps++
	b.stats.UnrecoveredPackets++
	return Slot{Sequence: seq, Class: SlotGap}
}

// Stats returns a snapshot of buffer counters.
func (b *JitterBuffer) Stats() Stats {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.stats
}
