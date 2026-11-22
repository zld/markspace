package transport

import (
	"sort"

	"github.com/pion/rtp"

	"mediadist/timeline"
)

// RecoveryMetrics exposes the jitter buffer counters used by the
// end-to-end report and cross-stage invariants.
type RecoveryMetrics struct {
	Received       int
	InOrder        int
	Reordered      int
	Duplicates     int
	Wrapped        int
	Missing        int
	Recovered      int
	Unrecoverable  int
	RejectedLate   int
}

// PushResult describes what one Push call changed. Output lists newly
// released, strictly ordered packets; ForceClosed lists extended
// sequence numbers declared unrecoverable during this call.
type PushResult struct {
	Duplicate   bool
	Output      []*rtp.Packet
	ForceClosed []uint64
}

// JitterBuffer reorders RTP packets within a bounded sequence window.
// Sequence holes are counted on forward jumps; holes filled in time are
// recovered, holes older than the window are declared unrecoverable.
// Placeholder media is never synthesized.
type JitterBuffer struct {
	stream  Stream
	window  uint64
	metrics RecoveryMetrics
	init    bool
	baseSeq uint16
	baseExt uint64
	nextSeq uint64
	highSeq uint64
	lastRaw uint16
	buf     map[uint64]*rtp.Packet
	seen    map[uint64]bool
	holes   map[uint64]bool
	dead    map[uint64]bool
}

// NewJitterBuffer builds a buffer for one SSRC. A zero window defaults to
// DefaultReorderWindow.
func NewJitterBuffer(stream Stream, window int) (*JitterBuffer, error) {
	if stream.SSRC == 0 || stream.ClockRate == 0 {
		return nil, timeline.NewError("INVALID_CONFIG", "stream requires SSRC and clock rate",
			map[string]any{"ssrc": stream.SSRC, "clock_rate": stream.ClockRate})
	}
	if window < 0 {
		return nil, timeline.NewError("INVALID_CONFIG", "reorder window must be >= 0",
			map[string]any{"window": window})
	}
	if window == 0 {
		window = DefaultReorderWindow
	}
	return &JitterBuffer{
		stream: stream,
		window: uint64(window),
		buf:    map[uint64]*rtp.Packet{},
		seen:   map[uint64]bool{},
		holes:  map[uint64]bool{},
		dead:   map[uint64]bool{},
	}, nil
}

// Metrics returns a copy of the counters.
func (b *JitterBuffer) Metrics() RecoveryMetrics { return b.metrics }

// Push ingests one parsed packet.
func (b *JitterBuffer) Push(p *rtp.Packet) (PushResult, error) {
	if p.SSRC != b.stream.SSRC {
		return PushResult{}, timeline.NewError("RTP_SSRC_MISMATCH",
			"packet does not belong to stream",
			map[string]any{"want": b.stream.SSRC, "got": p.SSRC})
	}
	if !b.init {
		b.init = true
		b.baseSeq, b.baseExt = p.SequenceNumber, 0
		b.nextSeq, b.highSeq, b.lastRaw = 0, 0, p.SequenceNumber
		b.buf[0], b.seen[0] = p, true
		b.metrics.Received++
		return PushResult{Output: b.drain()}, nil
	}

	ext := ExtSeq(b.baseExt, b.baseSeq, p.SequenceNumber)
	switch {
	case b.seen[ext]:
		b.metrics.Duplicates++
		return PushResult{Duplicate: true}, nil
	case b.dead[ext]:
		b.metrics.RejectedLate++
		return PushResult{}, timeline.NewError("RTP_UNRECOVERABLE_LATE",
			"packet arrived after its sequence hole was declared unrecoverable",
			map[string]any{"ext_seq": ext})
	}
	if p.SequenceNumber < b.lastRaw && ext > b.highSeq {
		b.metrics.Wrapped++
	}
	b.lastRaw = p.SequenceNumber
	b.seen[ext] = true
	b.buf[ext] = p
	b.metrics.Received++

	if b.holes[ext] {
		delete(b.holes, ext)
		b.metrics.Reordered++
		b.metrics.Recovered++
	}
	if ext > b.highSeq {
		for seq := b.highSeq + 1; seq < ext; seq++ {
			if !b.seen[seq] && !b.dead[seq] {
				b.holes[seq] = true
				b.metrics.Missing++
			}
		}
		b.highSeq = ext
	}
	out := b.drain()
	closed := b.forceClose()
	out = append(out, b.drain()...)
	return PushResult{Output: out, ForceClosed: closed}, nil
}

// drain releases consecutive available packets, skipping dead holes.
func (b *JitterBuffer) drain() []*rtp.Packet {
	var out []*rtp.Packet
	for {
		if b.dead[b.nextSeq] {
			b.nextSeq++
			continue
		}
		p, ok := b.buf[b.nextSeq]
		if !ok {
			return out
		}
		out = append(out, p)
		delete(b.buf, b.nextSeq)
		b.nextSeq++
		b.metrics.InOrder++
	}
}

func (b *JitterBuffer) minBuffered() (uint64, bool) {
	if len(b.buf) == 0 {
		return 0, false
	}
	seqs := make([]uint64, 0, len(b.buf))
	for k := range b.buf {
		seqs = append(seqs, k)
	}
	sort.Slice(seqs, func(i, j int) bool { return seqs[i] < seqs[j] })
	return seqs[0], true
}

// forceClose promotes holes older than the reorder window to
// unrecoverable and advances past them so buffered packets drain.
func (b *JitterBuffer) forceClose() []uint64 {
	var closed []uint64
	for {
		min, ok := b.minBuffered()
		if !ok || min-b.nextSeq <= b.window {
			return closed
		}
		for seq := b.nextSeq; seq < min; seq++ {
			if b.holes[seq] {
				delete(b.holes, seq)
				b.dead[seq] = true
				b.metrics.Unrecoverable++
				closed = append(closed, seq)
			}
		}
		b.nextSeq = min
	}
}

// Flush ends the stream: every still-open hole is declared
// unrecoverable and all buffered packets are released in order.
func (b *JitterBuffer) Flush() ([]*rtp.Packet, []uint64) {
	var missing []uint64
	if !b.init {
		return nil, missing
	}
	for seq := b.nextSeq; seq <= b.highSeq; seq++ {
		if b.holes[seq] {
			delete(b.holes, seq)
			b.dead[seq] = true
			b.metrics.Unrecoverable++
			missing = append(missing, seq)
		}
	}
	return b.drain(), missing
}

// RecoveryRate returns Recovered/Missing over all distinct lost
// packets. With no loss it reports 1.0.
func (m RecoveryMetrics) RecoveryRate() float64 {
	if m.Missing == 0 {
		return 1.0
	}
	return float64(m.Recovered) / float64(m.Missing)
}
