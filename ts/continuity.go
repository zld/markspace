package ts

import (
	"fmt"

	"mediad/internal/clock"
)

// ContinuityEvent describes one per-PID continuity counter anomaly.
type ContinuityEvent struct {
	PID       uint16
	Kind      clock.Kind // KindContinuity for jumps; duplicates are counted only
	WantCC    uint8
	GotCC     uint8
	// PacketRange is the half-open range of packet ordinal numbers involved.
	PacketRange [2]int64
	// OffsetRange is the same span expressed as byte offsets in the input.
	OffsetRange [2]int64
}

// pidState remembers the last continuity state for one PID.
type pidState struct {
	lastCC    uint8
	lastPkt   int64
	lastOff   int64
	have      bool
}

// ContinuityTracker validates 4-bit continuity counters per PID.
//
// A packet whose counter equals the previous one is a duplicate: it is
// reported via Duplicates and ignored by downstream stages. Any other
// non-incrementing counter is a jump and recorded with its PID and the range
// of packets it spans. Null packets (PID 0x1fff) carry no counter contract.
type ContinuityTracker struct {
	states     map[uint16]*pidState
	duplicates int
	// Events holds every non-duplicate discontinuity in arrival order.
	Events []ContinuityEvent
}

// NewContinuityTracker returns an empty tracker.
func NewContinuityTracker() *ContinuityTracker {
	return &ContinuityTracker{states: map[uint16]*pidState{}}
}

// Duplicates reports the number of duplicate packets seen so far.
func (t *ContinuityTracker) Duplicates() int { return t.duplicates }

// Feed validates one packet. pktOrdinal is the zero-based index of the packet
// in the full input (including non-tracked PIDs). The returned bool is false
// when the packet is a duplicate that callers must ignore.
func (t *ContinuityTracker) Feed(p Packet, pktOrdinal int64) (duplicate bool) {
	if p.PID == PIDNull {
		return false
	}
	st, ok := t.states[p.PID]
	if !ok {
		t.states[p.PID] = &pidState{lastCC: p.CC, lastPkt: pktOrdinal, lastOff: p.Offset}
		return false
	}
	expected := st.lastCC + 1
	switch {
	case p.CC == st.lastCC:
		t.duplicates++
		return true
	case p.CC != expected&0x0f:
		t.Events = append(t.Events, ContinuityEvent{
			PID:         p.PID,
			Kind:        clock.KindContinuity,
			WantCC:      expected & 0x0f,
			GotCC:       p.CC,
			PacketRange: [2]int64{st.lastPkt, pktOrdinal + 1},
			OffsetRange: [2]int64{st.lastOff, p.Offset + PacketSize},
		})
	}
	st.lastCC, st.lastPkt, st.lastOff = p.CC, pktOrdinal, p.Offset
	return false
}

// Report renders one event as the project's structured range error so the
// analysis subsystems share a single error model.
func (e ContinuityEvent) Report() clock.RangeError {
	return clock.RangeError{
		Kind:  e.Kind,
		Start: e.OffsetRange[0],
		End:   e.OffsetRange[1],
		Msg: fmt.Sprintf("pid 0x%04x continuity counter jumped: want %d got %d",
			e.PID, e.WantCC, e.GotCC),
	}
}
