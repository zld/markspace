// Package rtp parses the locally generated RTP transport stream. It wraps
// pion/rtp packet handling with a reordering buffer that understands 16-bit
// sequence wraparound and 32-bit timestamp wraparound, counts duplicates
// without emitting them twice, and marks unresolvable gaps so downstream
// segments can be flagged incomplete.
package rtp

import (
	"errors"

	"github.com/pion/rtp"

	"mediad/internal/clock"
	"mediad/internal/fault"
)

// Packet is a parsed RTP packet plus its zero-based byte offset inside the
// aggregate transport stream, used for fault reporting.
type Packet struct {
	*rtp.Packet
	StreamOffset int64
	WireLength   int
}

// Parse decodes one RTP datagram. Malformed packets produce a fault error
// carrying the supplied stream offset.
func Parse(wire []byte, streamOffset int64) (*Packet, error) {
	var p rtp.Packet
	if err := p.Unmarshal(wire); err != nil {
		return nil, fault.Wrap(fault.CatRTPPacket, "rtp.parse", streamOffset,
			-1, true, err)
	}
	if p.Version != 2 {
		return nil, fault.Wrap(fault.CatRTPPacket, "rtp.parse", streamOffset,
			-1, true, errors.New("unsupported RTP version"))
	}
	return &Packet{
		Packet:       &p,
		StreamOffset: streamOffset,
		WireLength:   len(wire),
	}, nil
}

// ReorderBuffer accepts packets in arbitrary arrival order and yields them in
// absolute sequence order. The zero value is not usable; use NewReorderBuffer.
type ReorderBuffer struct {
	timeline       *clock.Timestamp
	maxReorder     uint16
	buf            map[uint64]*Packet
	nextAbsSeq     uint64
	initialized    bool
	duplicateCount int
	gapCount       int
	firstOffset    int64
}

// Gap describes an unresolvable sequence-number interval and the byte offset
// at which the hole was detected.
type Gap struct {
	AbsSeqStart uint64
	AbsSeqEnd   uint64 // half-open
	Offset      int64
}

// NewReorderBuffer builds a buffer tolerating up to maxReorder packets of
// disorder. framerate and framesPerTS configure the shared timeline.
func NewReorderBuffer(framerate, framesPerTS uint32, maxReorder uint16) *ReorderBuffer {
	if maxReorder == 0 {
		maxReorder = 16
	}
	return &ReorderBuffer{
		timeline:    clock.NewTimestamp(framerate, framesPerTS),
		maxReorder:  maxReorder,
		buf:         make(map[uint64]*Packet),
		firstOffset: -1,
	}
}

// Push inserts one parsed packet. A duplicate (same absolute sequence number)
// increments the duplicate counter and is otherwise ignored.
func (b *ReorderBuffer) Push(p *Packet) error {
	if p == nil {
		return fault.New(fault.CatRTPPacket, "rtp.push", true, "nil packet")
	}
	absSeq := b.timeline.ObserveSeq(p.SequenceNumber)
	b.timeline.ObserveTS(p.Timestamp)
	if !b.initialized {
		b.initialized = true
		b.nextAbsSeq = absSeq
		b.firstOffset = p.StreamOffset
	} else if absSeq < b.nextAbsSeq {
		// Already delivered or already queued: a retransmission/duplicate.
		if _, queued := b.buf[absSeq]; !queued {
			b.duplicateCount++
		}
		return nil
	}
	if _, exists := b.buf[absSeq]; exists {
		b.duplicateCount++
		return nil
	}
	b.buf[absSeq] = p
	return nil
}

// Drain returns every contiguous run of packets starting at the expected
// sequence index, unwrapping timestamps onto the shared timeline. Holes that
// cannot be filled within the reorder window are returned as gaps; callers
// must treat every segment overlapping a gap as incomplete.
func (b *ReorderBuffer) Drain() ([]*Packet, []Gap) {
	var out []*Packet
	var gaps []Gap
	for {
		p, ok := b.buf[b.nextAbsSeq]
		if !ok {
			smallest := b.smallestQueued()
			if smallest == ^uint64(0) {
				break // nothing queued yet; wait for more arrivals
			}
			// A hole sits directly at the delivery cursor. Promote it once the
			// furthest queued packet is old enough that missing packets can no
			// longer arrive within the reorder window.
			if b.highestQueued()-b.nextAbsSeq <= uint64(b.maxReorder) {
				break
			}
			gaps = append(gaps, Gap{
				AbsSeqStart: b.nextAbsSeq,
				AbsSeqEnd:   smallest,
				Offset:      b.firstOffset,
			})
			b.gapCount += int(smallest - b.nextAbsSeq)
			b.nextAbsSeq = smallest
			continue
		}
		delete(b.buf, b.nextAbsSeq)
		out = append(out, p)
		b.nextAbsSeq++
	}
	return out, gaps
}

func (b *ReorderBuffer) highestQueued() uint64 {
	var hi uint64
	for k := range b.buf {
		if k > hi {
			hi = k
		}
	}
	return hi
}

func (b *ReorderBuffer) smallestQueued() uint64 {
	lo := ^uint64(0)
	for k := range b.buf {
		if k < lo {
			lo = k
		}
	}
	return lo
}

// DuplicateCount reports how many duplicate packets were suppressed.
func (b *ReorderBuffer) DuplicateCount() int { return b.duplicateCount }

// GapCount reports the total number of sequence numbers never recovered.
func (b *ReorderBuffer) GapCount() int { return b.gapCount }

// Timeline exposes the shared sample-frame timeline.
func (b *ReorderBuffer) Timeline() *clock.Timestamp { return b.timeline }

// Flush forces delivery of everything queued and reports any remaining hole
// as a gap. It is used at end-of-stream when no more reordering is possible.
func (b *ReorderBuffer) Flush() ([]*Packet, []Gap) {
	var gaps []Gap
	// At end of stream no late packets can arrive, so every hole smaller than
	// the smallest queued index is permanently unrecoverable.
	for {
		if _, queued := b.buf[b.nextAbsSeq]; queued {
			break // cursor position is fillable; let Drain emit it
		}
		smallest := b.smallestQueued()
		if smallest == ^uint64(0) {
			break // nothing remains queued
		}
		gaps = append(gaps, Gap{
			AbsSeqStart: b.nextAbsSeq,
			AbsSeqEnd:   smallest,
			Offset:      b.firstOffset,
		})
		b.gapCount += int(smallest - b.nextAbsSeq)
		b.nextAbsSeq = smallest
	}
	out, drainGaps := b.Drain()
	allGaps := make([]Gap, 0, len(gaps)+len(drainGaps))
	allGaps = append(allGaps, gaps...)
	allGaps = append(allGaps, drainGaps...)
	return out, allGaps
}
