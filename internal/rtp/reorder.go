package rtp

// WindowSize is the fixed RTP sequence window (spec: 64).
const WindowSize = 64

// EventKind classifies reorder output.
type EventKind uint8

const (
	// EventPacket is an in-order RTP record ready for downstream use.
	EventPacket EventKind = iota
	// EventLost marks one sequence number never seen as the window passes it.
	// No audio is fabricated for the gap; consumers bridge with silence.
	EventLost
)

// Event is one ordered reorder output element.
type Event struct {
	Kind   EventKind
	Seq    uint16 // sequence number (also valid for EventLost)
	Record *Record
}

// Stats summarizes reorder decisions.
type Stats struct {
	Received   int64 // packets pushed (including duplicates/late)
	InOrder    int64 // packets emitted as EventPacket
	Duplicates int64 // same sequence seen again inside the window: counted, dropped
	Late       int64 // arrived after the window moved past the sequence
	Lost       int64 // sequence numbers never filled when the window advanced
}

// ReorderBuffer reorders RTP packets within a fixed 64-slot sequence window.
// It is single-goroutine: the pipeline serializes pushes through one reader.
type ReorderBuffer struct {
	slots  [WindowSize]*Record
	seqs   [WindowSize]uint16
	filled [WindowSize]bool
	base   uint16 // next sequence number to emit
	init   bool
	stats  Stats
}

// NewReorderBuffer creates an empty buffer.
func NewReorderBuffer() *ReorderBuffer { return &ReorderBuffer{} }

// Push inserts a record and returns any events that become releasable.
// Duplicates are counted and dropped; out-of-window late packets are counted
// as late and dropped; forward gaps are emitted as EventLost when reached.
func (b *ReorderBuffer) Push(rec *Record) []Event {
	b.stats.Received++
	seq := rec.Packet.SequenceNumber
	if !b.init {
		b.init = true
		b.base = seq
	}
	dist := int32(int16(seq - b.base)) // signed modular distance
	switch {
	case dist < 0:
		// window already moved past this sequence number
		b.stats.Late++
	case dist >= WindowSize:
		return b.forwardJump(rec, seq, dist)
	default:
		slot := int(seq) % WindowSize
		if b.filled[slot] && b.seqs[slot] == seq {
			b.stats.Duplicates++
			return nil
		}
		b.slots[slot], b.seqs[slot], b.filled[slot] = rec, seq, true
	}
	return b.drain(false)
}

// forwardJump handles a packet more than a window ahead: everything between
// the old base and this sequence is lost, then the packet becomes the new base.
func (b *ReorderBuffer) forwardJump(rec *Record, seq uint16, dist int32) []Event {
	var ev []Event
	// flush everything currently buffered, marking empty slots as lost
	ev = append(ev, b.drain(true)...)
	// remaining gap from current base up to seq
	for b.base != seq {
		ev = append(ev, Event{Kind: EventLost, Seq: b.base})
		b.stats.Lost++
		b.base++
	}
	b.resetSlots()
	slot := int(seq) % WindowSize
	b.slots[slot], b.seqs[slot], b.filled[slot] = rec, seq, true
	return append(ev, b.drain(false)...)
}

// resetSlots clears every window slot.
func (b *ReorderBuffer) resetSlots() {
	for i := range b.filled {
		b.filled[i] = false
		b.slots[i] = nil
	}
}

// drain emits from the base while its slot is filled. When flush is true,
// unfilled slots at the front are emitted as lost and the base advances too.
func (b *ReorderBuffer) drain(flush bool) []Event {
	var ev []Event
	for {
		slot := int(b.base) % WindowSize
		if b.filled[slot] {
			ev = append(ev, Event{Kind: EventPacket, Seq: b.base, Record: b.slots[slot]})
			b.filled[slot] = false
			b.slots[slot] = nil
			b.stats.InOrder++
			b.base++
			continue
		}
		if !flush {
			return ev
		}
		// only count as lost if some later slot still holds data in this window
		if !b.windowHasLaterData() {
			return ev
		}
		ev = append(ev, Event{Kind: EventLost, Seq: b.base})
		b.stats.Lost++
		b.base++
	}
}

// windowHasLaterData reports whether any slot ahead of base is occupied.
func (b *ReorderBuffer) windowHasLaterData() bool {
	for k := 1; k < WindowSize; k++ {
		s := uint16(int(b.base) + k)
		slot := int(s) % WindowSize
		if b.filled[slot] && b.seqs[slot] == s {
			return true
		}
	}
	return false
}

// Flush ends a stream: remaining buffered packets leave in sequence order and
// trailing gaps are reported as lost.
func (b *ReorderBuffer) Flush() []Event {
	if !b.init {
		return nil
	}
	return b.drain(true)
}

// Stats returns a copy of the running counters.
func (b *ReorderBuffer) Stats() Stats { return b.stats }

// Base returns the next expected sequence number.
func (b *ReorderBuffer) Base() (uint16, bool) { return b.base, b.init }
