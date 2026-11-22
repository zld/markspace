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
	Ext        int64
	Recovered  bool // filled a hole that had already been declared missing
	Reordered  bool // arrived below the highest sequence seen so far
}

// Config bounds the receiver's buffers.
type Config struct {
	// MaxReorderWindow is how far behind the highest sequence a packet may
	// arrive and still be considered recoverable. Holes older than this when
	// the stream advances are declared unrecoverable, but a packet that
	// arrives anyway is still delivered and marked Recovered.
	MaxReorderWindow int
	// MaxExtend is the largest unsigned sequence jump treated as a wrap or
	// burst rather than a stream restart. Zero means 1<<15.
	MaxExtend int
	// MaxBuffer caps packets held by the receiver.
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
	Start uint16
	Count int
}

// Receiver accepts packets in arrival order and produces the sequence order.
//
// Output is materialised by Close rather than streamed as soon as it is
// contiguous: network jitter can put the first arrival several slots into the
// stream, and committing to a delivery point would turn that skew into
// phantom loss. Holding the packets and sorting at Close keeps loss
// accounting exact while the reorder window still governs which holes are
// classified recoverable.
type Receiver struct {
	cfg      Config
	started  bool
	highest  int64
	minExt   int64
	base     int64
	seen     map[int64]ExtPacket
	order    []int64 // arrival order of accepted extended sequences
	arrivals uint64

	dups       int
	reordered  int
	recovered  int
	wrapCount  int
	lost       int
	gaps       []Gap
	lateBeyond int
	latencies  []media.Nano
}

// New builds a Receiver.
func New(cfg Config) (*Receiver, error) {
	if cfg.MaxExtend == 0 {
		cfg.MaxExtend = 1 << 15
	}
	if err := cfg.Validate(); err != nil {
		return nil, err
	}
	return &Receiver{cfg: cfg, seen: make(map[int64]ExtPacket)}, nil
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

// Push accepts a packet in arrival order.
func (r *Receiver) Push(pkt *rtp.Packet, arrival media.Nano) error {
	r.arrivals++
	ext, err := r.extend(pkt.SequenceNumber)
	if err != nil {
		return err
	}
	if _, dup := r.seen[ext]; dup {
		r.dups++
		return nil
	}
	if len(r.seen) >= r.cfg.MaxBuffer {
		return media.NewError(media.ReplayLimitExceeded,
			"receiver holds %d packets (limit %d)", len(r.seen), r.cfg.MaxBuffer).
			AtSeq(uint32(pkt.SequenceNumber))
	}
	if !r.started {
		r.started = true
		r.highest = ext
		r.minExt = ext
		r.base = ext &^ 0xffff
	} else {
		if ext > r.highest {
			r.highest = ext
			r.base = r.highest &^ 0xffff
		}
		if ext < r.minExt {
			r.minExt = ext
		}
	}
	reordered := r.started && ext < r.highest
	ep := ExtPacket{
		Packet:     pkt,
		ArrivalSeq: r.arrivals,
		Arrival:    arrival,
		Seq:        pkt.SequenceNumber,
		Timestamp:  pkt.Timestamp,
		Track:      uint32(pkt.PayloadType),
		Ext:        ext,
		Reordered:  reordered,
	}
	if reordered {
		r.reordered++
		// A hole this packet fills is recoverable when the packet arrived
		// within the reorder window; beyond it, the hole was already
		// classified unrecoverable and the packet is flagged too-late.
		behind := r.highest - ext
		if behind > int64(r.cfg.MaxReorderWindow) {
			r.lateBeyond++
			ep.Recovered = false
		} else {
			ep.Recovered = true
			r.recovered++
		}
	}
	r.seen[ext] = ep
	r.order = append(r.order, ext)
	return nil
}

// Close finalises the stream: it sorts accepted packets by sequence, records
// every hole between the lowest and highest sequence as an unrecoverable gap,
// and returns those gaps.
func (r *Receiver) Close() []Gap {
	if !r.started {
		return nil
	}
	exts := make([]int64, 0, len(r.seen))
	for ext := range r.seen {
		exts = append(exts, ext)
	}
	sort.Slice(exts, func(i, j int) bool { return exts[i] < exts[j] })
	r.order = exts
	r.gaps = r.gaps[:0]
	r.lost = 0
	prev := r.minExt - 1
	for _, ext := range exts {
		if ext > prev+1 {
			r.recordGap(uint16(prev+1), int(ext-prev-1))
		}
		prev = ext
	}
	return r.gaps
}

// recordGap appends a missing run, merging with an adjacent preceding gap.
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

// Delivered returns packets in extended sequence order. Close must have been
// called for the order to be final.
func (r *Receiver) Delivered() []ExtPacket {
	out := make([]ExtPacket, 0, len(r.order))
	for _, ext := range r.order {
		if ep, ok := r.seen[ext]; ok {
			out = append(out, ep)
		}
	}
	return out
}

// Gaps returns unrecoverable gaps (valid after Close).
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
	LateBeyondWindow  int
	RecoveryRate      float64
	P95Latency        media.Nano
	MaxLatency        media.Nano
	UnrecoverableGaps int
}

// Stats summarises the receiver. RecoveryRate is delivered/(delivered+lost),
// so it measures how much of the sequence space known to have existed was
// actually handed downstream.
func (r *Receiver) Stats() Stats {
	s := Stats{
		Received:          int(r.arrivals),
		Delivered:         len(r.seen),
		Lost:              r.lost,
		Duplicates:        r.dups,
		Reordered:         r.reordered,
		Wraps:             r.wrapCount,
		Recovered:         r.recovered,
		LateBeyondWindow:  r.lateBeyond,
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
