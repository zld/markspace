package rtpreplay

import "fmt"

// Packet is one RTP packet as received, with its arrival event time in
// integer milliseconds. ArrivalMs is the trace clock, not a wall-clock stamp.
type Packet struct {
	Track     string
	Seq       uint16
	Timestamp uint32
	Marker    bool
	Payload   []byte
	ArrivalMs int64
	Fault     string
}

// EmissionKind distinguishes a delivered packet from a declared gap.
type EmissionKind int

const (
	EmitPacket EmissionKind = iota + 1
	EmitGap
)

// Emission is one ordered step of the reconstructed stream. A gap carries the
// number of consecutive missing sequence numbers and no audio: the replay
// never synthesizes samples for a gap.
type Emission struct {
	Kind   EmissionKind
	Packet *Packet
	ExtSeq uint64
	Count  uint64
}

// WindowStats counts arrival-layer anomalies. Every received packet is
// accounted for exactly once as Delivered, Duplicate, or Late.
type WindowStats struct {
	Received   int
	Delivered  int
	Duplicates int
	Late       int
	Reordered  int
	GapPackets uint64
}

// ReorderBuffer expands 16-bit RTP sequence numbers to a monotonic 64-bit
// space, classifies duplicates and reordering, and emits packets in order once
// the reorder window allows. Sequence numbers still missing when the window
// closes are emitted as gaps.
type ReorderBuffer struct {
	depth   uint64
	hasRef  bool
	last    uint64 // extended seq of the most recent expansion reference
	lastRaw uint16
	started bool
	next    uint64
	max     uint64
	// seen retains every observed position for the whole trace so that a
	// retransmission after a declared gap is still classified as duplicate.
	seen  map[uint64]struct{}
	buf   map[uint64]*Packet
	stats WindowStats
	out   []Emission
}

// NewReorderBuffer creates a buffer with the given reorder depth: a position
// is held until the highest observed sequence number is at least depth beyond
// it, at which point it is declared missing.
func NewReorderBuffer(depth int) (*ReorderBuffer, error) {
	if depth < 0 || depth > 4096 {
		return nil, NewError(CodeInvalidConfig, "reorder_buffer", fmt.Sprintf("depth %d out of range", depth))
	}
	return &ReorderBuffer{
		depth: uint64(depth),
		seen:  make(map[uint64]struct{}),
		buf:   make(map[uint64]*Packet),
	}, nil
}

// extend maps a raw sequence number onto the extended sequence space using
// signed 16-bit distance from the previous reference, which handles wrap.
func (b *ReorderBuffer) extend(seq uint16) uint64 {
	if !b.hasRef {
		b.hasRef = true
		b.lastRaw = seq
		b.last = uint64(seq)
		return b.last
	}
	delta := int64(int16(seq - b.lastRaw))
	ext := uint64(int64(b.last) + delta)
	b.lastRaw = seq
	b.last = ext
	return ext
}

// Push accepts one arrival. Wire-level anomalies are counted, not fatal; the
// only error is an internal invariant violation.
func (b *ReorderBuffer) Push(p *Packet) error {
	if p == nil {
		return NewError(CodeInvalidInput, "reorder_buffer", "nil packet")
	}
	ext := b.extend(p.Seq)
	b.stats.Received++
	if _, dup := b.seen[ext]; dup {
		b.stats.Duplicates++
		return nil
	}
	b.seen[ext] = struct{}{}
	if !b.started {
		b.started = true
		b.next = ext
		b.max = ext
	}
	if ext < b.next {
		// The window already closed over this position and a gap was
		// declared: keep the timeline stable and count the late arrival.
		b.stats.Late++
		return nil
	}
	if ext < b.max {
		b.stats.Reordered++
	}
	if ext > b.max {
		b.max = ext
	}
	b.buf[ext] = p
	b.drain(false)
	return nil
}

// drain emits positions that are decidable: buffered packets up to the window
// edge, and gaps for absent positions that the window has already passed.
func (b *ReorderBuffer) drain(final bool) {
	for b.started && b.next <= b.max {
		if _, ok := b.buf[b.next]; !ok {
			if !final && b.max-b.next < b.depth {
				return
			}
			b.out = append(b.out, Emission{Kind: EmitGap, ExtSeq: b.next, Count: 1})
			b.stats.GapPackets++
			b.next++
			continue
		}
		p := b.buf[b.next]
		delete(b.buf, b.next)
		b.out = append(b.out, Emission{Kind: EmitPacket, Packet: p, ExtSeq: b.next})
		b.stats.Delivered++
		b.next++
	}
}

// Finish flushes the window: remaining buffered packets are delivered and all
// still-missing sequence numbers become gaps. It returns the ordered stream.
func (b *ReorderBuffer) Finish() ([]Emission, WindowStats) {
	b.drain(true)
	if n := len(b.buf); n > 0 {
		// Unreachable while drain is correct, but never silently drop audio.
		b.stats.Late += n
	}
	return b.out, b.stats
}

// GapRun is a maximal run of consecutive missing sequence numbers.
type GapRun struct {
	StartExtSeq uint64
	EndExtSeq   uint64
	Count       uint64
}

// AggregateGaps returns the consecutive-gap runs of an ordered stream.
func AggregateGaps(emissions []Emission) []GapRun {
	var runs []GapRun
	for _, e := range emissions {
		if e.Kind != EmitGap {
			continue
		}
		if n := len(runs); n > 0 && runs[n-1].EndExtSeq+1 == e.ExtSeq {
			runs[n-1].EndExtSeq = e.ExtSeq + e.Count - 1
			runs[n-1].Count += e.Count
			continue
		}
		runs = append(runs, GapRun{
			StartExtSeq: e.ExtSeq,
			EndExtSeq:   e.ExtSeq + e.Count - 1,
			Count:       e.Count,
		})
	}
	return runs
}
