package transport

import "github.com/pion/rtp"

// GapRecord describes a committed run of missing RTP sequence numbers.
type GapRecord struct {
	First uint16 // first missing sequence number
	Count int    // run length
	Burst bool   // Count reached the configured burst threshold
}

// Stats accumulates reorderer counters.
type Stats struct {
	Received   uint64
	Duplicates uint64 // duplicate packets dropped
	Late       uint64 // arrived outside the reorder window, dropped
	Reordered  uint64 // out-of-order packets recovered into the stream
	Emitted    uint64
	GapEvents  uint64 // committed gap records
	BurstGaps  uint64 // gap records at or above the burst threshold
	GapPackets uint64 // total missing packets inside gaps
}

// seqDiff returns a-b in wrap-aware RFC 1982 arithmetic.
func seqDiff(a, b uint16) int16 { return int16(a - b) }

// Reorderer deduplicates and reorders one RTP stream with a bounded window.
// Memory is bounded by the window size plus a fixed delivered-history ring.
type Reorderer struct {
	StreamID       uint32
	Window         int // current reorder window, tuned by the bandwidth estimator
	BurstThreshold int

	next    uint16
	started bool
	buf     map[uint16]*rtp.Packet

	seen   map[uint16]struct{} // delivered or buffered seqs, bounded ring
	ring   []uint16
	ringAt int
	nSeen  int

	gapActive bool
	gapFirst  uint16
	gapCount  int

	Stats Stats
}

// NewReorderer builds a reorderer; window must be >= 1.
func NewReorderer(streamID uint32, window, burstThreshold int) *Reorderer {
	if window < 1 {
		window = 1
	}
	if burstThreshold < 1 {
		burstThreshold = 1
	}
	return &Reorderer{
		StreamID:       streamID,
		Window:         window,
		BurstThreshold: burstThreshold,
		buf:            make(map[uint16]*rtp.Packet, window),
		seen:           make(map[uint16]struct{}, 4*window),
		ring:           make([]uint16, 4*window),
	}
}

// Push feeds one packet; it returns packets ready to emit in order and any
// gap records committed by this push. Duplicates and too-late packets are
// counted and dropped, never emitted twice.
func (r *Reorderer) Push(p *rtp.Packet) (out []*rtp.Packet, gaps []GapRecord) {
	out, _, gaps = r.PushEx(p)
	return out, gaps
}

// PushEx is Push plus the sequence numbers recovered from the reorder
// buffer (arrived out of order but before their commit point).
func (r *Reorderer) PushEx(p *rtp.Packet) (out []*rtp.Packet, recovered []uint16, gaps []GapRecord) {
	r.Stats.Received++
	seq := p.SequenceNumber
	if !r.started {
		r.next = seq
		r.started = true
	}
	if _, dup := r.seen[seq]; dup {
		r.Stats.Duplicates++
		return nil, nil, nil
	}
	if seqDiff(seq, r.next) < 0 {
		r.Stats.Late++
		return nil, nil, nil
	}
	// Force advancement while the packet sits beyond the window.
	for seqDiff(seq, r.next) >= int16(r.Window) {
		out, gaps = r.step(out, gaps)
	}
	if seqDiff(seq, r.next) != 0 {
		r.Stats.Reordered++
		recovered = append(recovered, seq)
	}
	r.buf[seq] = p
	r.remember(seq)
	for {
		pkt, ok := r.buf[r.next]
		if !ok {
			break
		}
		delete(r.buf, r.next)
		out = append(out, pkt)
		r.Stats.Emitted++
		gaps = r.closeGap(gaps)
		r.next++
	}
	return out, recovered, gaps
}

// step commits one sequence position: emit if buffered, else extend the gap.
func (r *Reorderer) step(out []*rtp.Packet, gaps []GapRecord) ([]*rtp.Packet, []GapRecord) {
	if pkt, ok := r.buf[r.next]; ok {
		delete(r.buf, r.next)
		out = append(out, pkt)
		r.Stats.Emitted++
		gaps = r.closeGap(gaps)
	} else {
		if !r.gapActive {
			r.gapActive = true
			r.gapFirst = r.next
			r.gapCount = 0
		}
		r.gapCount++
		r.Stats.GapPackets++
	}
	r.remember(r.next)
	r.next++
	return out, gaps
}

// Flush drains the buffer at end of stream and closes any open gap.
func (r *Reorderer) Flush() (out []*rtp.Packet, gaps []GapRecord) {
	for len(r.buf) > 0 {
		out, gaps = r.step(out, gaps)
	}
	return out, r.closeGap(gaps)
}

func (r *Reorderer) closeGap(gaps []GapRecord) []GapRecord {
	if !r.gapActive {
		return gaps
	}
	g := GapRecord{First: r.gapFirst, Count: r.gapCount, Burst: r.gapCount >= r.BurstThreshold}
	r.Stats.GapEvents++
	if g.Burst {
		r.Stats.BurstGaps++
	}
	r.gapActive = false
	r.gapCount = 0
	return append(gaps, g)
}

// remember records a sequence number in the bounded dedup ring.
func (r *Reorderer) remember(seq uint16) {
	if r.nSeen >= len(r.ring) {
		delete(r.seen, r.ring[r.ringAt])
	}
	r.ring[r.ringAt] = seq
	r.ringAt = (r.ringAt + 1) % len(r.ring)
	r.nSeen++
	r.seen[seq] = struct{}{}
}
