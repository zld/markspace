// Package rtp receives pion RTP packets, extends 16-bit sequence numbers to
// a continuous 64-bit space (RFC 3550 appendix A.1 style), removes
// duplicates inside a reorder window, and reports out-of-window gaps as
// losses without fabricating the missing media.
package rtp

import (
	"github.com/pion/rtp"

	"media-check/internal/fault"
)

// DefaultWindowSize is the maximum out-of-order distance the buffer absorbs.
// Packets arriving more than this many sequence numbers late are declared
// lost rather than buffered forever.
const DefaultWindowSize = 64

// OrderedPacket is a packet emitted by the buffer in extended-sequence order.
type OrderedPacket struct {
	// ExtSeq is the unwrapped, monotonically increasing sequence number.
	ExtSeq uint64
	Packet *rtp.Packet
}

// Gap is an inclusive range of sequence numbers never delivered.
type Gap struct {
	Start uint64
	End   uint64
}

// ReorderBuffer reorders one SSRC's packet stream.
type ReorderBuffer struct {
	ssrc        uint32
	trackID     uint32
	windowSize  uint64
	initialized bool
	// base is the next extended sequence the consumer expects to see.
	base uint64
	// maxSeq is the highest extended sequence already accepted.
	maxSeq uint64
	// finalized is the highest sequence whose fate (arrived/lost) is decided.
	finalized    uint64
	finalizedSet bool
	// expectedStart/hasExpectedStart anchor the sender's first sequence so a
	// lost leading packet is still reported.
	expectedStart      uint64
	hasExpectedStart   bool
	extMapped          bool
	leadingGapRecorded bool
	// buffer holds packets not yet emitted, keyed by extended sequence.
	buffer map[uint64]*rtp.Packet
	// received marks every accepted extended seq; entries below base are
	// pruned as positions get finalized.
	received map[uint64]bool
	faults   *fault.Model
	lostGaps []Gap
	dupes    int
}

// NewReorderBuffer constructs a buffer bound to one SSRC/track.
func NewReorderBuffer(trackID uint32, ssrc uint32, windowSize int, fm *fault.Model) *ReorderBuffer {
	if windowSize <= 0 {
		windowSize = DefaultWindowSize
	}
	return &ReorderBuffer{
		ssrc:       ssrc,
		trackID:    trackID,
		windowSize: uint64(windowSize),
		buffer:     make(map[uint64]*rtp.Packet),
		received:   make(map[uint64]bool),
		faults:     fm,
	}
}

// SetExpectedStart anchors the sequence number the sender's first packet
// would carry. When the first observed packet is later than this anchor, the
// leading missing run is recorded as a loss gap instead of being invisible.
// RTCP sender reports supply the anchor in this pipeline.
func (b *ReorderBuffer) SetExpectedStart(startExtSeq uint64) {
	b.expectedStart = startExtSeq
	b.hasExpectedStart = true
}

// Push accepts one parsed packet. A packet inside the window is buffered;
// a duplicate is counted once and discarded. It returns the packets that can
// now be emitted contiguously from the current base, in order.
func (b *ReorderBuffer) Push(p *rtp.Packet) []OrderedPacket {
	if !b.initialized {
		b.initialized = true
		if b.hasExpectedStart {
			// Anchor the base at the sender's expected first sequence. Map
			// the first observed 16-bit seq relative to that anchor so wrap
			// at the stream start is handled correctly.
			b.base = b.expectedStart
			b.maxSeq = b.expectedStart
		} else {
			b.base = uint64(p.SequenceNumber)
			b.maxSeq = b.base
		}
	}
	var ext uint64
	var ambiguous bool
	if b.hasExpectedStart && !b.extMapped {
		ext = mapFirstSeq(b.expectedStart, p.SequenceNumber)
		b.extMapped = true
		ambiguous = uint16(uint32(p.SequenceNumber)-uint32(uint16(b.expectedStart))) == 0x8000
	} else {
		ext, ambiguous = b.extendSeq(p.SequenceNumber)
	}
	if ambiguous {
		b.faults.Record(fault.Fault{
			Code:     fault.CodeSequenceWrap,
			Severity: fault.SeverityFatal,
			TrackID:  b.trackID,
			SeqStart: ext,
			SeqEnd:   ext,
			Message:  "sequence jump reaches half the 16-bit space; wrap cannot be decided uniquely",
		})
	}

	// Before the next expected position: duplicate in flight, or a
	// retransmission arriving after its gap was finalized as loss.
	if ext < b.base {
		b.recordDuplicate(ext)
		return nil
	}
	if b.received[ext] {
		b.recordDuplicate(ext)
		return nil
	}
	b.received[ext] = true
	b.buffer[ext] = p
	if ext > b.maxSeq {
		b.maxSeq = ext
	}
	b.ageOut()
	return b.drain()
}

// Flush finalizes the stream: remaining holes become permanent losses and
// every buffered packet is emitted in extended-sequence order.
func (b *ReorderBuffer) Flush() []OrderedPacket {
	b.finalizeThrough(b.maxSeq)
	return b.drain()
}

// Stats returns observed duplicate count and finalized loss gaps.
func (b *ReorderBuffer) Stats() (duplicates int, gaps []Gap) {
	return b.dupes, append([]Gap(nil), b.lostGaps...)
}

// Base exposes the next expected extended sequence (for tests/inspection).
func (b *ReorderBuffer) Base() uint64 { return b.base }

// drain emits every buffered packet at or above base, skipping over sequence
// positions already finalized as permanent loss.
func (b *ReorderBuffer) drain() []OrderedPacket {
	var out []OrderedPacket
	// Walk through every decided position: present packets are emitted,
	// missing ones were recorded as loss by finalizeThrough and are simply
	// skipped, never replaced with fabricated media.
	if b.finalizedSet {
		for b.base <= b.finalized {
			if p, ok := b.buffer[b.base]; ok {
				out = append(out, OrderedPacket{ExtSeq: b.base, Packet: p})
				delete(b.buffer, b.base)
			}
			delete(b.received, b.base)
			b.base++
		}
	}
	// Then emit any fully-present contiguous run ahead of finalized.
	for {
		p, ok := b.buffer[b.base]
		if !ok {
			break
		}
		out = append(out, OrderedPacket{ExtSeq: b.base, Packet: p})
		delete(b.buffer, b.base)
		delete(b.received, b.base)
		b.base++
	}
	return out
}

// ageOut finalizes gaps once the highest accepted packet is at least a full
// window ahead of the next expected position: a hole that old will never be
// filled in time, so its missing packets are declared lost.
func (b *ReorderBuffer) ageOut() {
	if b.maxSeq >= b.base+b.windowSize {
		b.finalizeThrough(b.maxSeq - b.windowSize)
	}
}

// finalizeThrough marks every missing sequence up to limit as a loss gap.
func (b *ReorderBuffer) finalizeThrough(limit uint64) {
	if limit > b.maxSeq {
		return
	}
	if b.finalizedSet && limit <= b.finalized {
		return
	}
	start := b.base
	if b.finalizedSet && b.finalized+1 > start {
		start = b.finalized + 1
	}
	var gapStart uint64
	inGap := false
	for seq := start; seq <= limit; seq++ {
		if b.received[seq] {
			if inGap {
				b.closeGap(gapStart, seq-1)
				inGap = false
			}
			continue
		}
		if !inGap {
			gapStart = seq
			inGap = true
		}
	}
	if inGap {
		b.closeGap(gapStart, limit)
	}
	b.finalized = limit
	b.finalizedSet = true
}

func (b *ReorderBuffer) closeGap(start, end uint64) {
	if end < start {
		return
	}
	b.lostGaps = append(b.lostGaps, Gap{Start: start, End: end})
	b.faults.Record(fault.Fault{
		Code:      fault.CodeLost,
		Severity:  fault.SeverityFatal,
		TrackID:   b.trackID,
		SeqStart:  start,
		SeqEnd:    end,
		Message:   "sequence gap finalized: packets not delivered",
		Recovered: false,
	})
}

func (b *ReorderBuffer) recordDuplicate(ext uint64) {
	b.dupes++
	b.faults.Record(fault.Fault{
		Code:     fault.CodeDuplicate,
		Severity: fault.SeverityWarning,
		TrackID:  b.trackID,
		SeqStart: ext,
		SeqEnd:   ext,
		Message:  "duplicate or late packet counted once and never re-emitted",
	})
}

// mapFirstSeq places the first observed 16-bit sequence onto the extended
// space anchored at the sender's expected start, applying the signed modular
// delta so a wrap at the very beginning is unrolled.
func mapFirstSeq(expectedStart uint64, seq uint16) uint64 {
	anchor16 := uint16(expectedStart)
	modDelta := uint16(uint32(seq) - uint32(anchor16))
	delta := int16(modDelta)
	if delta >= 0 {
		return expectedStart + uint64(delta)
	}
	return expectedStart + uint64(int64(0x10000)+int64(delta))
}

// extendSeq maps a 16-bit RTP sequence number into continuous 64-bit space.
// The modular difference interpreted as a signed 16-bit value gives the
// shortest signed jump (WebRTC/RFC 3550 practice): a forward wrap such as
// 65535 -> 0 appears as +1, while a genuinely late packet appears negative.
// A jump reaching half the 16-bit range cannot be disambiguated and is
// flagged, although a value is still produced so scanning can continue.
func (b *ReorderBuffer) extendSeq(seq uint16) (uint64, bool) {
	if !b.initialized {
		return uint64(seq), false
	}
	prev16 := uint16(b.maxSeq & 0xFFFF)
	modDelta := uint16(uint32(seq) - uint32(prev16))
	ambiguous := modDelta == 0x8000
	delta := int16(modDelta) // signed: -32768..32767
	switch {
	case delta >= 0:
		return b.maxSeq + uint64(delta), ambiguous
	case b.maxSeq >= uint64(-delta):
		return b.maxSeq - uint64(-delta), ambiguous
	default:
		// Backward jump before stream origin: keep 0 and let the caller
		// treat it as a late/duplicate packet.
		return 0, true
	}
}
