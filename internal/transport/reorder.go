package transport

import (
	"github.com/pion/rtp"

	"archivecheck/internal/merr"
)

// ReorderBuffer reassembles one segment stream in order using a bounded
// window. Semantics:
//
//   - duplicate sequence numbers are counted once (DupCount);
//   - out-of-order packets arriving within Window slots of the highest
//     seen sequence are buffered and emitted in order when the gap fills;
//   - once a missing sequence is older than the highest sequence by more
//     than Window slots, the window slides past it and every skipped
//     sequence is reported as a real gap (LostCount), never filled in;
//   - Flush at end-of-segment drains the buffer and counts any
//     still-missing sequences between the first and last packet.
type ReorderBuffer struct {
	Window  int
	TrackID uint32
	SegSeq  uint32

	started   bool
	nextSeq   uint32 // next expected unwrapped sequence
	highest   uint32 // highest unwrapped sequence observed
	pending   map[uint32]*rtp.Packet
	Received  int // unique packets accepted
	DupCount  int
	LostCount int
}

// NewReorderBuffer creates a buffer. Window must be >= 1.
func NewReorderBuffer(window int, trackID, segSeq uint32) (*ReorderBuffer, error) {
	if window < 1 {
		return nil, merr.New(merr.KindArgument, "transport",
			"track:"+u32(trackID)+":seq:"+u32(segSeq),
			"reorder window must be >= 1")
	}
	return &ReorderBuffer{
		Window:  window,
		TrackID: trackID,
		SegSeq:  segSeq,
		pending: make(map[uint32]*rtp.Packet),
	}, nil
}

// Push accepts one packet and returns the packets that became
// deliverable in strict sequence order. A burst loss is never hidden:
// skipped sequences are counted as lost when the window slides.
func (b *ReorderBuffer) Push(pkt *rtp.Packet) ([]*rtp.Packet, error) {
	if pkt == nil {
		return nil, merr.New(merr.KindDecode, "transport", b.location(),
			"nil packet pushed to reorder buffer")
	}
	if !b.started {
		b.started = true
		first := UnwrapSeq(0, pkt.SequenceNumber)
		b.nextSeq = first
		b.highest = first
	}
	seq := UnwrapSeq(b.highest, pkt.SequenceNumber)

	// Already delivered: a retransmission of an old sequence number.
	if seq < b.nextSeq {
		b.DupCount++
		return nil, nil
	}
	// Already buffered: duplicate inside the window.
	if _, exists := b.pending[seq]; exists {
		b.DupCount++
		return nil, nil
	}

	if seq > b.highest {
		b.highest = seq
	}
	b.pending[seq] = pkt
	b.Received++
	return b.drain(false), nil
}

// drain emits every contiguous packet starting at nextSeq and slides the
// window, counting sequences that fell outside it as lost.
func (b *ReorderBuffer) drain(final bool) []*rtp.Packet {
	var out []*rtp.Packet
	for {
		if pkt, ok := b.pending[b.nextSeq]; ok {
			delete(b.pending, b.nextSeq)
			out = append(out, pkt)
			b.nextSeq++
			continue
		}
		// Gap at nextSeq.
		if final || b.highest-b.nextSeq >= uint32(b.Window) {
			if b.nextSeq <= b.highest {
				b.LostCount += int(b.highest - b.nextSeq)
				b.nextSeq = b.highest
				continue
			}
			return out
		}
		return out
	}
}

// Flush marks end-of-segment: remaining gaps are real losses and the
// buffered packets are delivered in order.
func (b *ReorderBuffer) Flush() []*rtp.Packet {
	if !b.started {
		return nil
	}
	return b.drain(true)
}

// Stats describes recovery outcome for one segment.
type Stats struct {
	Received  int
	Duplicate int
	Lost      int
	Window    int
}

// Snapshot returns the current counters.
func (b *ReorderBuffer) Snapshot() Stats {
	return Stats{Received: b.Received, Duplicate: b.DupCount,
		Lost: b.LostCount, Window: b.Window}
}

func (b *ReorderBuffer) location() string {
	return "track:" + u32(b.TrackID) + ":seq:" + u32(b.SegSeq)
}

// Err returns a locatable transport-gap error when losses were recorded.
func (b *ReorderBuffer) Err() error {
	if b.LostCount == 0 {
		return nil
	}
	return merr.New(merr.KindTransport, "transport", b.location(),
		"burst loss: "+u32(uint32(b.LostCount))+
			" packet(s) outside reorder window "+u32(uint32(b.Window)))
}
