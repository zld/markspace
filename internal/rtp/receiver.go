// Package rtp receives synthetic RTP packets over a lossy, reordering,
// jittering channel and yields a sorted, gap-annotated event stream.
package rtp

import (
	"sort"

	"github.com/pion/rtp"

	"mediapipeline/internal/media"
)

// ExtPacket is a received packet plus the arrival metadata the pipeline needs.
type ExtPacket struct {
	Packet     *rtp.Packet
	ArrivalSeq uint64     // 1-based order in which the receiver observed it
	Arrival    media.Nano // simulated arrival timestamp
	Seq        uint16
	Timestamp  uint32
	Track      uint32
	Recovered  bool // arrived behind the current head and was reordered
}

// Config bounds the receiver's buffers.
type Config struct {
	// MaxReorderWindow is how many sequence slots behind the highest number
	// may still be filled before a gap is declared unrecoverable.
	MaxReorderWindow int
	// MaxExtend is the largest unsigned sequence jump treated as a wrap or
	// burst rather than a stream restart. Zero means 1<<15.
	MaxExtend int
	// MaxBuffer caps packets held pending reordering.
	MaxBuffer int
}

// Validate checks the configuration.
func (c Config) Validate() error {
	if c.MaxReorderWindow <= 0 {
		return media.NewError(media.InvalidInput, "reorder window must be positive")
	}
	if c.MaxExtend < 0 {
		return media.NewError(media.InvalidInput, "extend limit must be non-negative")
	}
	if c.MaxBuffer <= 0 {
		return media.NewError(media.InvalidInput, "buffer limit must be positive")
	}
	return nil
}

// Gap is an unrecoverable run of missing sequence numbers.
type Gap struct {
	Start uint16 // extended-ordered first missing 16-bit sequence number
	Count int
}

// Receiver reorders packets by extended sequence number, counts wraps and
// duplicates, fills bounded gaps, and declares the rest unrecoverable.
type Receiver struct {
	cfg Config

	started   bool
	next      int64 // lowest sequence not yet delivered
	highest   int64 // highest sequence number observed
	base      int64 // high bits of the extended sequence space
	buffered  map[int64]ExtPacket
	delivered []ExtPacket

	arrivals  uint64
	dups      int
	reordered int
	recovered int
	wrapCount int
	lost      int
	gaps      []Gap
	latencies []media.Nano
}

// New builds a Receiver.
func New(cfg Config) (*Receiver, error) {
	if cfg.MaxExtend == 0 {
		cfg.MaxExtend = 1 << 15
	}
	if err := cfg.Validate(); err != nil {
		return nil, err
	}
	return &Receiver{cfg: cfg, buffered: make(map[int64]ExtPacket)}, nil
}

// extend maps a 16-bit sequence onto the monotonic extended sequence space by
// choosing the representation closest to the highest sequence seen. A jump
// beyond MaxExtend means the stream restarted, which is reported rather than
// silently reordered.
func (r *Receiver) extend(seq uint16) (int64, error) {
	if !r.started {
		return int64(seq), nil
	}
	cur := r.base | int64(seq)
	best, bestDist := cur, int64(1)<<62
	for _, delta := range []int64{-1 << 16, 0, 1 << 16} {
		cand := cur + delta
		if cand < 0 {
			continue
		}
		dist := cand - r.highest
		if dist < 0 {
			dist = -dist
		}
		if dist < bestDist {
			best, bestDist = cand, dist
		}
	}
	if bestDist > int64(r.cfg.MaxExtend) {
		return 0, media.NewError(media.UnrecoverableGap,
			"sequence %d jumped %d slots from %d (limit %d)",
			seq, bestDist, uint16(r.highest), r.cfg.MaxExtend).AtSeq(uint32(seq))
	}
	if best>>16 != r.highest>>16 {
		r.wrapCount++
	}
	return best, nil
}

// Push accepts a packet in arrival order and delivers whatever becomes
// contiguous. Arrival order is deliberately independent of sequence order.
func (r *Receiver) Push(pkt *rtp.Packet, arrival media.Nano) error {
	r.arrivals++
	ext, err := r.extend(pkt.SequenceNumber)
	if err != nil {
		return err
	}
	if _, ok := r.buffered[ext]; ok {
		r.dups++
		return nil
	}
	if r.started && ext < r.next {
		// Already delivered: a retransmission, not new data.
		r.dups++
		return nil
	}
	if !r.started {
		r.started = true
		r.next = ext
		r.highest = ext
		r.base = ext &^ 0xffff
	} else if ext > r.highest {
		r.highest = ext
		r.base = r.highest &^ 0xffff
	}
	if ext < r.highest {
		r.reordered++
	}
	if len(r.buffered) >= r.cfg.MaxBuffer {
		return media.NewError(media.ReplayLimitExceeded,
			"reorder buffer holds %d packets", len(r.buffered)).AtSeq(uint32(pkt.SequenceNumber))
	}
	r.buffered[ext] = ExtPacket{
		Packet:     pkt,
		ArrivalSeq: r.arrivals,
		Arrival:    arrival,
		Seq:        pkt.SequenceNumber,
		Timestamp:  pkt.Timestamp,
		Track:      uint32(pkt.PayloadType),
		Recovered:  ext < r.highest,
	}
	r.drain()
	r.declareLost()
	return nil
}

// recordGap appends a missing run, merging it with an immediately preceding
// gap so progressive declaration does not fragment one loss into many.
func (r *Receiver) recordGap(start uint16, count int) {
	r.lost += count
	if n := len(r.gaps); n > 0 {
		prev := &r.gaps[n-1]
		if uint16(int(prev.Start)+prev.Count) == start {
			prev.Count += count
			return
		}
	}
	r.gaps = append(r.gaps, Gap{Start: start, Count: count})
}

// drain delivers contiguous buffered packets starting at next.
func (r *Receiver) drain() {
	for {
		ep, ok := r.buffered[r.next]
		if !ok {
			return
		}
		delete(r.buffered, r.next)
		r.delivered = append(r.delivered, ep)
		if ep.Recovered {
			r.recovered++
		}
		r.next++
	}
}

// declareLost advances past slots that can no longer be filled. It only fires
// once the highest observed sequence is more than the reorder window ahead,
// so a bounded number of lost packets is tolerated without stalling.
func (r *Receiver) declareLost() {
	for r.highest-r.next >= int64(r.cfg.MaxReorderWindow) {
		target := r.highest - int64(r.cfg.MaxReorderWindow) + 1
		if target <= r.next {
			return
		}
		r.recordGap(uint16(r.next), int(target-r.next))
		r.next = target
		r.drain()
	}
}

// Close declares every remaining buffered gap unrecoverable and delivers the
// buffered packets in order.
func (r *Receiver) Close() {
	slots := make([]int64, 0, len(r.buffered))
	for ext := range r.buffered {
		slots = append(slots, ext)
	}
	sort.Slice(slots, func(i, j int) bool { return slots[i] < slots[j] })
	for _, ext := range slots {
		if ext > r.next {
			r.recordGap(uint16(r.next), int(ext-r.next))
		}
		ep := r.buffered[ext]
		delete(r.buffered, ext)
		r.delivered = append(r.delivered, ep)
		if ep.Recovered {
			r.recovered++
		}
		r.next = ext + 1
	}
}

// Delivered returns packets in delivery (extended sequence) order.
func (r *Receiver) Delivered() []ExtPacket { return r.delivered }

// Gaps returns unrecoverable gaps.
func (r *Receiver) Gaps() []Gap { return r.gaps }

// Stats is the receiver scoreboard exposed to the CLI and provenance.
type Stats struct {
	Received          int
	Delivered         int
	Lost              int
	Duplicates        int
	Reordered         int
	Wraps             int
	Recovered         int
	RecoveryRate      float64
	P95Latency        media.Nano
	MaxLatency        media.Nano
	UnrecoverableGaps int
}

// Stats summarises the receiver. RecoveryRate is delivered/(delivered+lost).
func (r *Receiver) Stats() Stats {
	s := Stats{
		Received:          int(r.arrivals),
		Delivered:         len(r.delivered),
		Lost:              r.lost,
		Duplicates:        r.dups,
		Reordered:         r.reordered,
		Wraps:             r.wrapCount,
		Recovered:         r.recovered,
		UnrecoverableGaps: len(r.gaps),
	}
	if total := s.Delivered + s.Lost; total > 0 {
		s.RecoveryRate = float64(s.Delivered) / float64(total)
	}
	if len(r.latencies) > 0 {
		sorted := append([]media.Nano(nil), r.latencies...)
		sort.Slice(sorted, func(i, j int) bool { return sorted[i] < sorted[j] })
		idx := (len(sorted) * 95) / 100
		if idx >= len(sorted) {
			idx = len(sorted) - 1
		}
		s.P95Latency = sorted[idx]
		s.MaxLatency = sorted[len(sorted)-1]
	}
	return s
}

// NoteLatency records one measured end-to-end latency sample.
func (r *Receiver) NoteLatency(d media.Nano) {
	if d < 0 {
		return
	}
	r.latencies = append(r.latencies, d)
}
