// Package rtp turns raw RTP/RTCP datagrams into an ordered, fully
// expanded packet stream. It never invents media data: a sequence number
// that does not arrive is reported as a gap, not filled.
package rtp

import (
	"github.com/pion/rtp"

	"mediacheck/internal/fault"
)

// OrderedPacket is one delivered packet with its expanded identity.
type OrderedPacket struct {
	ExpandedSeq  uint64
	RawSeq       uint16
	ArrivalIndex int
	Packet       *rtp.Packet
}

// Gap is a span of sequence numbers that never arrived. OverWindow gaps
// were forced out by the bounded reorder window; ordinary gaps are the
// holes left when the stream ended.
type Gap struct {
	Seq        fault.SeqRange
	OverWindow bool
}

// seqExpander maps 16-bit sequence numbers onto a monotone uint64 axis.
// It expands relative to the highest sequence number seen so far, which
// keeps out-of-order arrivals (including packets just below a wrap)
// correct instead of assuming each packet follows the previous one.
type seqExpander struct {
	highestRaw uint16
	highestExp uint64
	seeded     bool
}

func (e *seqExpander) feed(v uint16) uint64 {
	if !e.seeded {
		e.seeded = true
		e.highestRaw = v
		e.highestExp = uint64(v)
		return e.highestExp
	}
	dist := int32(int16(v - e.highestRaw))
	exp := e.highestExp + uint64(int64(dist))
	if dist > 0 {
		e.highestRaw = v
		e.highestExp = exp
	}
	return exp
}

// Stats summarizes what a ReorderBuffer observed.
type Stats struct {
	Input          int
	Delivered      int
	Duplicates     int
	MissingPackets int
	OverWindowGaps int
}

// ReorderBuffer resequences packets within a bounded window.
//
// Invariant: base is the next seq to deliver; open is the first seq not
// yet accounted for (delivered or reported as a gap). Every seq below
// open is closed, so late arrivals for it are dropped as duplicates.
type ReorderBuffer struct {
	window  uint64
	exp     seqExpander
	hold    map[uint64]*OrderedPacket
	base    uint64
	max     uint64
	open    uint64
	seeded  bool
	arrival int
	stats   Stats
}

// NewReorderBuffer creates a buffer that tolerates up to windowSize
// packets of reordering delay. windowSize must be positive.
func NewReorderBuffer(windowSize int) *ReorderBuffer {
	if windowSize <= 0 {
		windowSize = 1
	}
	return &ReorderBuffer{
		window: uint64(windowSize),
		hold:   make(map[uint64]*OrderedPacket),
	}
}

// PushResult reports everything one Push produced.
type PushResult struct {
	Delivered []OrderedPacket
	Gaps      []Gap
	Duplicate bool
}

// Push accepts one parsed RTP packet. Packets below the closed frontier
// are duplicates and dropped; packets inside the window are buffered; a
// packet beyond the window closes the oldest holes as over-window gaps.
func (b *ReorderBuffer) Push(p *rtp.Packet) PushResult {
	b.stats.Input++
	b.arrival++
	seq := b.exp.feed(p.SequenceNumber)
	res := PushResult{}

	if !b.seeded {
		b.seeded = true
		b.base, b.max, b.open = seq, seq, seq
	}
	if seq < b.open {
		b.stats.Duplicates++
		res.Duplicate = true
		return res
	}
	if _, exists := b.hold[seq]; exists {
		b.stats.Duplicates++
		res.Duplicate = true
		return res
	}
	b.hold[seq] = &OrderedPacket{
		ExpandedSeq:  seq,
		RawSeq:       p.SequenceNumber,
		ArrivalIndex: b.arrival,
		Packet:       p,
	}
	if seq > b.max {
		b.max = seq
	}

	// A packet more than window positions ahead of the frontier forces
	// the oldest outstanding seq numbers out of the waiting window.
	if b.max >= b.base+b.window {
		newBase := b.max - b.window + 1
		for q := b.open; q < newBase; q++ {
			if _, ok := b.hold[q]; !ok {
				res.Gaps = append(res.Gaps, Gap{
					Seq:        fault.SeqRange{First: q, Last: q},
					OverWindow: true,
				})
			}
		}
		b.open = newBase
		b.base = newBase
	}
	res.Delivered = b.drainFrontier()
	res.Gaps = mergeGaps(res.Gaps)
	b.account(res)
	return res
}

// Flush marks end of stream: every still-open hole becomes an ordinary
// missing-packet gap and every buffered packet is delivered in order.
func (b *ReorderBuffer) Flush() PushResult {
	res := PushResult{}
	if !b.seeded {
		return res
	}
	for q := b.open; q <= b.max; q++ {
		if _, ok := b.hold[q]; !ok {
			res.Gaps = append(res.Gaps, Gap{
				Seq:        fault.SeqRange{First: q, Last: q},
				OverWindow: false,
			})
		}
	}
	b.open = b.max + 1
	b.base = b.max + 1
	// Held packets above the new base still belong to the caller; move
	// the base back down to the lowest buffered seq so they drain.
	if len(b.hold) > 0 {
		first := b.max + 1
		for q := range b.hold {
			if q < first {
				first = q
			}
		}
		b.base = first
	}
	res.Delivered = b.drainFrontier()
	res.Gaps = mergeGaps(res.Gaps)
	b.account(res)
	return res
}

func (b *ReorderBuffer) drainFrontier() []OrderedPacket {
	out := make([]OrderedPacket, 0)
	for {
		op, ok := b.hold[b.base]
		if !ok {
			return out
		}
		delete(b.hold, b.base)
		out = append(out, *op)
		b.base++
		if b.base > b.open {
			b.open = b.base
		}
	}
}

func (b *ReorderBuffer) account(res PushResult) {
	b.stats.Delivered += len(res.Delivered)
	for _, g := range res.Gaps {
		n := int(g.Seq.Len())
		b.stats.MissingPackets += n
		if g.OverWindow {
			b.stats.OverWindowGaps += n
		}
	}
}

// ExpandSeq maps a raw 16-bit sequence number to the buffer's expanded
// axis without delivering anything, so callers can label packets that
// cannot be fully parsed using the same coordinate system as gaps.
func (b *ReorderBuffer) ExpandSeq(raw uint16) uint64 {
	return b.exp.feed(raw)
}

// Stats returns a copy of the running counters.
func (b *ReorderBuffer) Stats() Stats { return b.stats }

// mergeGaps coalesces adjacent single-packet gaps into ranges, keeping
// the OverWindow flag uniform within one output range.
func mergeGaps(in []Gap) []Gap {
	if len(in) == 0 {
		return in
	}
	out := make([]Gap, 0, len(in))
	cur := in[0]
	for _, g := range in[1:] {
		if g.OverWindow == cur.OverWindow && g.Seq.First == cur.Seq.Last+1 {
			cur.Seq.Last = g.Seq.Last
			continue
		}
		out = append(out, cur)
		cur = g
	}
	return append(out, cur)
}

// DecodeDatagram parses one UDP datagram payload as an RTP packet. A
// short or otherwise invalid header returns an error that callers
// record as a localized truncated-header fault.
func DecodeDatagram(buf []byte) (*rtp.Packet, error) {
	p := &rtp.Packet{}
	if err := p.Unmarshal(buf); err != nil {
		return nil, err
	}
	return p, nil
}

// EncodeDatagram serializes an RTP packet back into a datagram payload.
func EncodeDatagram(p *rtp.Packet) ([]byte, error) {
	size := p.Header.MarshalSize() + len(p.Payload) + int(p.PaddingSize)
	buf := make([]byte, size)
	n, err := p.MarshalTo(buf)
	if err != nil {
		return nil, err
	}
	return buf[:n], nil
}
