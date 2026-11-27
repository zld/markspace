package timeline

import (
	"math"
	"math/bits"

	"github.com/pion/rtp"
)

const nsPerSec = 1_000_000_000

// ToNs converts raw ticks in the given timescale to nanoseconds on the
// unified timebase. Zero timescales and int64 overflows are rejected with
// structured errors; values are never silently corrected.
func ToNs(raw, timescale uint64, source string) (int64, error) {
	if timescale == 0 {
		return 0, New(KindZeroTimescale, "timeline.ToNs", source, 0,
			"raw=%d has zero timescale", raw)
	}
	hi, lo := bits.Mul64(raw, nsPerSec)
	if hi >= timescale {
		return 0, New(KindOverflow, "timeline.ToNs", source, 0,
			"raw=%d timescale=%d overflows int64 nanoseconds", raw, timescale)
	}
	q, _ := bits.Div64(hi, lo, timescale)
	if q > math.MaxInt64 {
		return 0, New(KindOverflow, "timeline.ToNs", source, 0,
			"raw=%d timescale=%d overflows int64 nanoseconds", raw, timescale)
	}
	return int64(q), nil
}

// Mapper tracks per-representation end positions and rejects events whose
// start regresses before the previous end (duplicate or backward events).
type Mapper struct {
	lastEnd map[string]int64
	seen    map[string]bool
}

// NewMapper returns an empty Mapper.
func NewMapper() *Mapper {
	return &Mapper{lastEnd: map[string]int64{}, seen: map[string]bool{}}
}

// Check validates that [startNs,endNs) does not move backwards within the
// representation, then records the interval.
func (m *Mapper) Check(rep string, startNs, endNs int64, source string, line int) error {
	if m.seen[rep] && startNs < m.lastEnd[rep] {
		return New(KindBackward, "timeline.Map", source, line,
			"representation %q start %d ns regresses before previous end %d ns",
			rep, startNs, m.lastEnd[rep])
	}
	m.seen[rep] = true
	if endNs > m.lastEnd[rep] {
		m.lastEnd[rep] = endNs
	}
	return nil
}

// RTPExtender extends 32-bit RTP timestamps to a 64-bit monotonic sequence
// using packet sequence numbers, for wrap-prone media clocks such as 90 kHz.
type RTPExtender struct {
	cycles  uint64
	prevTS  uint32
	prevSeq uint16
	started bool
}

// Extend returns the 64-bit extended timestamp for pkt. A timestamp decrease
// together with an advancing sequence number marks a 32-bit wrap.
func (e *RTPExtender) Extend(pkt *rtp.Packet) uint64 {
	if e.started && pkt.SequenceNumber != e.prevSeq && pkt.Timestamp < e.prevTS {
		e.cycles++
	}
	e.started = true
	e.prevTS = pkt.Timestamp
	e.prevSeq = pkt.SequenceNumber
	return e.cycles<<32 | uint64(pkt.Timestamp)
}
