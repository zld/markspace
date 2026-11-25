// Package rtp handles network-facing packet processing: bounded reorder,
// wrap-safe sequencing, bandwidth-adaptive buffering and XOR repair. All
// clocks stay on the master timeline and never run backwards.
package rtp

import (
	"github.com/local/medialab/internal/model"
	"github.com/local/medialab/internal/timeline"
)

// ReorderConfig bounds the buffer so memory use stays finite under burst
// loss and pathological reordering.
type ReorderConfig struct {
	WindowSize    int   // max sequence distance held before flushing
	MaxBuffered   int   // hard cap on packets retained
	BaseWaitTicks int64 // buffer wait at high bandwidth
	MinWaitTicks  int64 // floor so wait never collapses to zero
	MaxWaitTicks  int64 // ceiling so wait can never grow unbounded
}

// DefaultReorderConfig returns safe bounded defaults.
func DefaultReorderConfig() ReorderConfig {
	return ReorderConfig{
		WindowSize: 64, MaxBuffered: 4096,
		BaseWaitTicks: timeline.MasterHz / 100, // 10ms
		MinWaitTicks:  timeline.MasterHz / 200, // 5ms
		MaxWaitTicks:  timeline.MasterHz / 4,   // 250ms
	}
}

// arrival is one buffered packet with its simulated arrival tick.
type arrival struct {
	seq        uint16
	ts         uint32
	arriveTick int64
	payload    []byte
	repair     bool
}

// ReorderBuffer deduplicates by sequence number, sorts within a bounded
// window and reports late packets (outside the window) as losses. It is
// safe for sequential single-goroutine use.
type ReorderBuffer struct {
	cfg     ReorderConfig
	have    map[uint16]arrival
	order   []uint16 // sorted sequence numbers currently buffered
	nextSeq uint16   // next expected in-order sequence
	started bool
	// recentDelivered is a bounded ring of delivered seqs so duplicates of
	// already-flushed packets are still recognized.
	recentDelivered []uint16
	deliverHead     int
	// axis maps 32-bit RTP timestamps onto a monotonic master axis.
	tsAxis    int64
	lastTS    uint32
	tsStarted bool
	stats     ReorderStats
}

// ReorderStats exposes observable buffer behavior.
type ReorderStats struct {
	Received    int
	Duplicates  int
	Late        int
	Flushed     int
	MaxBuffered int
}

// NewReorderBuffer constructs an empty buffer.
func NewReorderBuffer(cfg ReorderConfig) *ReorderBuffer {
	return &ReorderBuffer{cfg: cfg, have: map[uint16]arrival{}}
}

// Push ingests one packet at the given simulated arrival tick. A duplicate
// sequence number is counted and ignored; a packet older than the window
// floor is counted late (treated as lost) and never buffered.
func (b *ReorderBuffer) Push(seq uint16, ts uint32, payload []byte, repair bool, arriveTick int64) {
	b.stats.Received++
	if !b.started {
		b.nextSeq = seq
		b.started = true
	}
	// Timestamp wrap handling: advance the monotonic axis, never reverse.
	if !b.tsStarted {
		b.tsAxis, b.lastTS, b.tsStarted = 0, ts, true
	} else {
		b.tsAxis = timeline.Monotonic32(b.tsAxis, b.lastTS, ts)
		b.lastTS = ts
	}

	if _, exists := b.have[seq]; exists {
		b.stats.Duplicates++
		return
	}
	if b.wasDelivered(seq) {
		b.stats.Duplicates++
		return
	}
	// Already delivered? Anything behind nextSeq by more than the window is
	// a window-external late packet.
	if b.started && timeline.Advance16(b.nextSeq, seq) < -int64(b.cfg.WindowSize) {
		b.stats.Late++
		return
	}
	if len(b.have) >= b.cfg.MaxBuffered {
		// Back-pressure: flush the oldest entry first via caller drain;
		// count the newcomer as late rather than growing without bound.
		b.stats.Late++
		return
	}
	b.have[seq] = arrival{seq: seq, ts: ts, arriveTick: arriveTick,
		payload: append([]byte(nil), payload...), repair: repair}
	b.insertSorted(seq)
	if len(b.have) > b.stats.MaxBuffered {
		b.stats.MaxBuffered = len(b.have)
	}
}

func (b *ReorderBuffer) insertSorted(seq uint16) {
	pos := len(b.order)
	for i, s := range b.order {
		if timeline.Advance16(seq, s) < 0 {
			pos = i
			break
		}
	}
	b.order = append(b.order, 0)
	copy(b.order[pos+1:], b.order[pos:])
	b.order[pos] = seq
}

// Drain returns every in-order packet available starting at nextSeq.
// Gaps stop the drain; the caller decides whether to wait or declare loss.
func (b *ReorderBuffer) Drain() []arrival {
	var out []arrival
	for {
		a, ok := b.have[b.nextSeq]
		if !ok {
			return out
		}
		out = append(out, a)
		delete(b.have, b.nextSeq)
		b.rememberDelivered(b.nextSeq)
		b.removeFront()
		b.nextSeq++
		b.stats.Flushed++
	}
}

// rememberDelivered keeps the last 4*WindowSize delivered sequence numbers
// so duplicate deliveries can be detected with bounded memory.
func (b *ReorderBuffer) rememberDelivered(seq uint16) {
	capacity := b.cfg.WindowSize * 4
	if capacity < 16 {
		capacity = 16
	}
	if len(b.recentDelivered) < capacity {
		b.recentDelivered = append(b.recentDelivered, seq)
		return
	}
	b.recentDelivered[b.deliverHead] = seq
	b.deliverHead = (b.deliverHead + 1) % len(b.recentDelivered)
}

// wasDelivered reports whether seq appears in the recent delivery ring.
func (b *ReorderBuffer) wasDelivered(seq uint16) bool {
	for _, s := range b.recentDelivered {
		if s == seq {
			return true
		}
	}
	return false
}

// AdvanceWindow forces the window past unresolved gaps: every still-missing
// sequence number older than WindowSize behind the newest buffered packet
// is declared lost and nextSeq jumps over it.
func (b *ReorderBuffer) AdvanceWindow(book *model.ErrorBook, windowFor func(seq uint16) timeline.Window) []uint16 {
	if len(b.order) == 0 {
		return nil
	}
	newest := b.order[len(b.order)-1]
	var lost []uint16
	for timeline.Advance16(b.nextSeq, newest) > int64(b.cfg.WindowSize) {
		if _, ok := b.have[b.nextSeq]; !ok {
			lost = append(lost, b.nextSeq)
			if book != nil {
				book.Add(model.ErrorRecord{
					Code: model.CodeRTPLost, Track: 1,
					Window: windowFor(b.nextSeq),
					Detail: "sequence gap never filled before window advanced",
				})
			}
			b.nextSeq++
			continue
		}
		// Present but window forced: drain it below.
		break
	}
	return lost
}

// Stats returns a copy of the running counters.
func (b *ReorderBuffer) Stats() ReorderStats { return b.stats }

// Buffered reports how many packets are currently held.
func (b *ReorderBuffer) Buffered() int { return len(b.have) }

// hasSeq reports whether a sequence is currently buffered (not yet drained).
func (b *ReorderBuffer) hasSeq(seq uint16) bool {
	_, ok := b.have[seq]
	return ok
}

func (b *ReorderBuffer) removeFront() {
	if len(b.order) > 0 {
		b.order = b.order[1:]
	}
}

// WaitForBandwidth maps an instantaneous bandwidth (kbps) to a bounded
// buffer wait in master ticks. Low bandwidth raises the wait to absorb
// jitter; high bandwidth lowers it. The clamp guarantees finite waiting.
func (cfg ReorderConfig) WaitForBandwidth(kbps int) int64 {
	if kbps <= 0 {
		return cfg.MaxWaitTicks
	}
	// Reference 1000kbps; wait scales inversely.
	w := cfg.BaseWaitTicks * 1000 / int64(kbps)
	if w < cfg.MinWaitTicks {
		w = cfg.MinWaitTicks
	}
	if w > cfg.MaxWaitTicks {
		w = cfg.MaxWaitTicks
	}
	return w
}

// TimestampAxis exposes the wrap-safe monotonic timestamp axis.
func (b *ReorderBuffer) TimestampAxis() int64 { return b.tsAxis }
