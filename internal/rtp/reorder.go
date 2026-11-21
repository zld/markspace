// Package rtp contains the project-owned RTP reordering and H.264
// reassembly logic. gortsplib only handles transport; everything in this
// package is transport-independent and fully deterministic.
package rtp

import (
	"errors"
	"fmt"

	"github.com/pion/rtp"
)

// DefaultWindowSize is the maximum number of sequence numbers the reorder
// buffer holds before it confirms missing packets and slides forward.
const DefaultWindowSize = 32

// Errors returned for packets that fail structural validation. They are
// counted but never panic or block the pipeline.
var (
	ErrVersion       = errors.New("rtp: unsupported version, only RTP/AVP version 2 is accepted")
	ErrExtension     = errors.New("rtp: header extension bit set, extensions are not supported")
	ErrPadding       = errors.New("rtp: padding size exceeds payload length")
	ErrEmptyPayload  = errors.New("rtp: empty payload")
	ErrPayloadTooBig = errors.New("rtp: payload exceeds configured packet size limit")
	ErrTruncated     = errors.New("rtp: truncated packet")
)

// ReorderStats holds the counters produced while reordering one stream.
type ReorderStats struct {
	// SequenceGaps counts individual sequence numbers confirmed missing,
	// either because the window was exhausted or the stream was flushed.
	SequenceGaps int
	// DuplicatePackets counts packets whose sequence number was already
	// delivered, already buffered or already confirmed missing.
	DuplicatePackets int
	// MalformedPackets counts packets rejected by validation.
	MalformedPackets int
}

// Reorderer re-establishes RTP sequence order using a bounded window.
// Sequence numbers are compared with 16-bit modular arithmetic, so a
// wrap-around from 65535 to 0 is handled transparently. The zero value is
// not usable; construct one with NewReorderer.
type Reorderer struct {
	windowSize int
	maxPayload int

	initialized bool
	next        uint16
	buf         map[uint16]*rtp.Packet

	stats ReorderStats
}

// NewReorderer creates a reorderer. windowSize must be in [1, 1024] and
// maxPayload caps the accepted RTP payload length.
func NewReorderer(windowSize, maxPayload int) *Reorderer {
	return &Reorderer{
		windowSize: windowSize,
		maxPayload: maxPayload,
		buf:        make(map[uint16]*rtp.Packet),
	}
}

// seqDelta returns the signed modular distance from base to seq, in the
// range [-32768, 32767], per RFC 3550 section 3.5.1 arithmetic.
func seqDelta(seq, base uint16) int32 {
	return int32(int16(seq - base))
}

// Validate enforces the structural rules the pipeline relies on. It never
// indexes into the payload beyond its declared length.
func Validate(pkt *rtp.Packet, maxPayload int) error {
	if pkt.Version != 2 {
		return ErrVersion
	}
	if pkt.Extension {
		return ErrExtension
	}
	if int(pkt.PaddingSize) > len(pkt.Payload) {
		return ErrPadding
	}
	if len(pkt.Payload) == 0 {
		return ErrEmptyPayload
	}
	if len(pkt.Payload) > maxPayload {
		return ErrPayloadTooBig
	}
	return nil
}

// Parse unmarshals a raw datagram and validates it. It is a hard boundary:
// malformed bytes produce an error instead of an out-of-bounds read.
func Parse(raw []byte, maxPayload int) (*rtp.Packet, error) {
	pkt := &rtp.Packet{}
	if len(raw) < 12 {
		return nil, fmt.Errorf("%w: %d bytes", ErrTruncated, len(raw))
	}
	if err := pkt.Unmarshal(raw); err != nil {
		return nil, fmt.Errorf("%w: %v", ErrTruncated, err)
	}
	if err := Validate(pkt, maxPayload); err != nil {
		return nil, err
	}
	return pkt, nil
}

// Process feeds one validated packet and returns the packets that have
// become deliverable in strict sequence order. The returned slice is empty
// when the packet was buffered, a duplicate or the first packet seen.
func (r *Reorderer) Process(pkt *rtp.Packet) ([]*rtp.Packet, error) {
	if err := Validate(pkt, r.maxPayload); err != nil {
		r.stats.MalformedPackets++
		return nil, err
	}

	seq := pkt.SequenceNumber

	if !r.initialized {
		r.initialized = true
		r.next = seq
	}

	delta := seqDelta(seq, r.next)

	switch {
	case delta < 0:
		// Older than the next expected number: it was delivered, buffered
		// earlier or already confirmed missing. In every case it is a
		// duplicate and must never open a new gap.
		r.stats.DuplicatePackets++
		return nil, nil

	case delta >= int32(r.windowSize):
		// Window exhausted: everything before (seq-windowSize+1) is now
		// confirmed missing, then the window slides forward.
		confirmedNext := uint16(int(seq) - r.windowSize + 1)
		r.advance(confirmedNext)
	}

	if _, exists := r.buf[seq]; exists {
		r.stats.DuplicatePackets++
		return nil, nil
	}
	r.buf[seq] = pkt

	return r.drain(), nil
}

// advance moves the expected sequence number to target, counting every
// skipped sequence number as a confirmed gap. Buffered packets are neither
// deleted nor delivered here: the pointer passes them and a following
// drain call delivers the ones that are now due in order.
func (r *Reorderer) advance(target uint16) {
	for r.next != target {
		if _, ok := r.buf[r.next]; !ok {
			r.stats.SequenceGaps++
		}
		r.next++
	}
}

// drain emits every consecutively available packet starting at next and
// removes it from the buffer, so the map can never grow beyond the window.
func (r *Reorderer) drain() []*rtp.Packet {
	var out []*rtp.Packet
	for {
		pkt, ok := r.buf[r.next]
		if !ok {
			return out
		}
		delete(r.buf, r.next)
		r.next++
		out = append(out, pkt)
	}
}

// Flush ends the stream: remaining buffered packets are delivered in order
// and the sequence numbers between them are confirmed as gaps.
func (r *Reorderer) Flush() []*rtp.Packet {
	if !r.initialized || len(r.buf) == 0 {
		return nil
	}

	// Highest buffered sequence number, computed with modular comparison.
	var maxSeq uint16
	first := true
	for seq := range r.buf {
		if first || seqDelta(seq, maxSeq) > 0 {
			maxSeq = seq
			first = false
		}
	}
	// Every missing number up to the highest buffered packet is a gap.
	for seq := r.next; seq != maxSeq; seq++ {
		if _, ok := r.buf[seq]; !ok {
			r.stats.SequenceGaps++
		}
	}

	// Collect the remaining packets in sequence order, skipping the holes
	// counted above. The buffer is then emptied so it cannot retain data.
	out := make([]*rtp.Packet, 0, len(r.buf))
	for seq := r.next; seqDelta(seq, maxSeq) <= 0; seq++ {
		if pkt, ok := r.buf[seq]; ok {
			out = append(out, pkt)
		}
	}
	r.buf = make(map[uint16]*rtp.Packet)
	r.next = uint16(maxSeq + 1)
	return out
}

// Stats returns a copy of the accumulated counters.
func (r *Reorderer) Stats() ReorderStats {
	return r.stats
}
