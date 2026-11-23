// Package rtp implements per-stream RTP de-duplication, reordering and loss
// classification. Duplicate packets are counted once; out-of-order packets
// arriving inside the reorder window are delivered in sequence order; bursts
// that fit the recovery window are marked recoverable, while a packet that
// arrives after the window advanced past it is recorded as an unrecoverable
// gap. RTP timestamps are unwrapped to a monotonic 64-bit counter.
package rtp

import (
	"sync"

	"github.com/pion/rtp"

	"mediakernel/internal/clock"
	"mediakernel/internal/mediaerr"
)

// Config bounds one reorder buffer.
type Config struct {
	Window uint16 // reorder window in sequence slots
}

// Stats is the per-stream recovery ledger.
type Stats struct {
	StreamID           string `json:"stream_id"`
	Received           int    `json:"received"`            // unique packets accepted
	Duplicates         int    `json:"duplicates"`          // repeated SSRC+seq, not counted again
	Reordered          int    `json:"reordered"`           // arrivals that filled an existing hole
	RecoverableBursts  int    `json:"recoverable_bursts"`  // distinct gap events seen within window
	UnrecoveredPackets int    `json:"unrecovered_packets"` // slots abandoned past the window
	LatePackets        int    `json:"late_packets"`        // arrivals after the window moved on
}

// RecoveryRate is unique accepted / (unique accepted + unrecovered slots).
func (s Stats) RecoveryRate() float64 {
	denom := s.Received + s.UnrecoveredPackets
	if denom == 0 {
		return 1
	}
	return float64(s.Received) / float64(denom)
}

// Ordered is one packet released in sequence order.
type Ordered struct {
	Packet       *rtp.Packet
	MonotonicTS  uint64 // unwrapped 32-bit RTP timestamp
	WasReordered bool
}

// streamBuffer holds the state machine for one SSRC.
type streamBuffer struct {
	cfg       Config
	stats     Stats
	slots     map[uint16]*rtp.Packet
	delivered map[uint16]struct{} // sequence numbers already released
	lastTS    uint64
	nextSeq   uint16 // next sequence number expected in order
	started   bool
	inBurst   bool // a frontier hole is currently open
}

// Reorderer keeps one independent buffer per stream (SSRC-derived id).
type Reorderer struct {
	mu      sync.Mutex
	cfg     Config
	streams map[string]*streamBuffer
	order   []string
}

// NewReorderer validates the window and constructs an empty reorderer.
func NewReorderer(cfg Config) (*Reorderer, error) {
	if cfg.Window == 0 {
		return nil, mediaerr.New(mediaerr.CatValidation, "window_zero", "reorder window must be positive")
	}
	if int(cfg.Window) > mediaerr.MaxReorderWindow {
		return nil, mediaerr.New(mediaerr.CatValidation, "window_cap", "reorder window exceeds limit").
			Wrap(mediaerr.ErrLimit)
	}
	return &Reorderer{cfg: cfg, streams: map[string]*streamBuffer{}}, nil
}

// Push accepts one packet for stream id and returns the packets that become
// deliverable, in strict sequence order, plus any structured transport
// errors. A duplicate yields no output and is counted once; a late packet
// past the window is counted and reported, not delivered.
func (r *Reorderer) Push(streamID string, pkt *rtp.Packet) ([]Ordered, []*mediaerr.Error) {
	r.mu.Lock()
	defer r.mu.Unlock()

	sb := r.streams[streamID]
	if sb == nil {
		if len(r.streams) >= mediaerr.MaxStreams {
			return nil, []*mediaerr.Error{mediaerr.New(mediaerr.CatValidation, "stream_cap",
				"too many RTP streams").Wrap(mediaerr.ErrLimit)}
		}
		sb = &streamBuffer{
			cfg:       r.cfg,
			stats:     Stats{StreamID: streamID},
			slots:     map[uint16]*rtp.Packet{},
			delivered: map[uint16]struct{}{},
		}
		r.streams[streamID] = sb
		r.order = append(r.order, streamID)
	}
	return sb.push(pkt)
}

func (sb *streamBuffer) push(pkt *rtp.Packet) ([]Ordered, []*mediaerr.Error) {
	var errs []*mediaerr.Error
	seq := pkt.SequenceNumber

	mono, err := clock.UnwrapTS(sb.lastTS, uint64(pkt.Timestamp), 32)
	if err != nil {
		return nil, []*mediaerr.Error{mediaerr.New(mediaerr.CatTimebase, "ts_unwrap", err.Error())}
	}
	sb.lastTS = mono

	if !sb.started {
		sb.started = true
		sb.nextSeq = seq
	} else {
		dist := clock.SeqDistance(sb.nextSeq, seq) // >0: newer than frontier
		switch {
		case dist < 0:
			// Older than the frontier: a buffered copy is a duplicate; an
			// already-delivered slot is a harmless duplicate; anything else
			// arrived too late to be re-integrated.
			if _, buffered := sb.slots[seq]; buffered {
				sb.stats.Duplicates++
				return nil, nil
			}
			if _, done := sb.delivered[seq]; done {
				sb.stats.Duplicates++
				return nil, nil
			}
			sb.stats.LatePackets++
			sb.stats.UnrecoveredPackets++
			errs = append(errs, mediaerr.New(mediaerr.CatTransport, "late_packet",
				"packet arrived after the reorder window closed").
				With(sb.stats.StreamID, mono, false))
			return nil, errs
		case dist == 0:
			if _, buffered := sb.slots[seq]; buffered {
				sb.stats.Duplicates++
				return nil, nil
			}
			if _, done := sb.delivered[seq]; done {
				sb.stats.Duplicates++
				return nil, nil
			}
		case dist > int(sb.cfg.Window):
			// Frontier jumps beyond the window. Enumerate every slot in
			// (nextSeq, seq): buffered packets were received and are
			// accounted as delivered; only genuinely absent slots are
			// unrecoverable. This makes the loss count exact rather than
			// deriving it from a depth that later re-counts the same hole.
			prev := sb.nextSeq
			missing := 0
			// Candidate absent slots are [nextSeq, seq): exactly dist slots.
			for d := 0; d < dist; d++ {
				cand := prev + uint16(d)
				if p, ok := sb.slots[cand]; ok {
					// Already received out of order: count it, release it.
					sb.delivered[cand] = struct{}{}
					delete(sb.slots, cand)
					_ = p
				} else if _, done := sb.delivered[cand]; !done {
					missing++
				}
			}
			sb.nextSeq = seq
			if missing > 0 {
				sb.stats.UnrecoveredPackets += missing
			}
			errs = append(errs, mediaerr.New(mediaerr.CatTransport, "burst_unrecoverable",
				"sequence gap exceeds reorder window").
				With(sb.stats.StreamID, mono, false))
		}
	}

	if _, exists := sb.slots[seq]; exists {
		sb.stats.Duplicates++
		return nil, nil
	}
	if _, done := sb.delivered[seq]; done {
		sb.stats.Duplicates++
		return nil, nil
	}

	// Filling a slot strictly ahead of the frontier means a hole was open.
	fillsHole := sb.started && clock.SeqDistance(sb.nextSeq, seq) > 0
	sb.slots[seq] = pkt
	sb.stats.Received++
	if fillsHole {
		sb.stats.Reordered++
	}

	out := sb.drain()

	// Account for the contiguous hole at the frontier. A burst is counted
	// once when it first opens; filling the frontier closes the state.
	depth := sb.frontierHoleDepth()
	switch {
	case depth == 0:
		sb.inBurst = false
	case depth <= int(sb.cfg.Window) && !sb.inBurst:
		sb.inBurst = true
		sb.stats.RecoverableBursts++
	}
	return out, errs
}

// drain releases every contiguous packet starting at the frontier. A packet
// is flagged reordered when the buffer still holds packets ahead of it, i.e.
// it was held back to fill a hole rather than arriving at the frontier.
func (sb *streamBuffer) drain() []Ordered {
	var out []Ordered
	for {
		p, ok := sb.slots[sb.nextSeq]
		if !ok {
			return out
		}
		out = append(out, Ordered{
			Packet:       p,
			MonotonicTS:  sb.lastTS,
			WasReordered: sb.hasAhead(),
		})
		delete(sb.slots, sb.nextSeq)
		sb.delivered[sb.nextSeq] = struct{}{}
		sb.nextSeq++
	}
}

// hasAhead reports whether any buffered slot is newer than the frontier.
func (sb *streamBuffer) hasAhead() bool {
	for seq := range sb.slots {
		if clock.SeqDistance(sb.nextSeq, seq) > 0 {
			return true
		}
	}
	return false
}

// frontierHoleDepth counts contiguous missing slots from the frontier up to
// the window bound. A buffer with no pending slots (all traffic drained) has
// no hole by definition.
func (sb *streamBuffer) frontierHoleDepth() int {
	if len(sb.slots) == 0 {
		return 0
	}
	depth := 0
	for depth < int(sb.cfg.Window) {
		if _, ok := sb.slots[sb.nextSeq+uint16(depth)]; ok {
			break
		}
		depth++
	}
	return depth
}

// Flush marks the stream end: remaining gaps become unrecoverable and the
// buffered packets are released in order.
func (r *Reorderer) Flush(streamID string) ([]Ordered, Stats, []*mediaerr.Error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	sb := r.streams[streamID]
	if sb == nil {
		return nil, Stats{}, nil
	}
	var errs []*mediaerr.Error
	// At end of stream the contiguous hole at the frontier can no longer be
	// filled, so every missing slot up to the next buffered packet is
	// unrecoverable. With no buffered packets remaining there is no tail gap.
	if len(sb.slots) > 0 {
		// Find the nearest buffered sequence ahead of the frontier.
		var nearest uint16
		have := false
		for s := range sb.slots {
			if !have || clock.SeqDistance(sb.nextSeq, s) < clock.SeqDistance(sb.nextSeq, nearest) {
				nearest, have = s, true
			}
		}
		if have {
			gap := clock.SeqDistance(sb.nextSeq, nearest) // slots strictly between
			for d := 0; d < gap; d++ {
				miss := sb.nextSeq + uint16(d)
				if _, done := sb.delivered[miss]; !done {
					sb.stats.UnrecoveredPackets++
				}
			}
			if gap > 0 {
				errs = append(errs, mediaerr.New(mediaerr.CatTransport, "tail_gap",
					"stream ended with an unfilled sequence gap").
					With(streamID, sb.lastTS, false))
			}
			sb.nextSeq = nearest
		}
	}
	out := sb.drain()
	return out, sb.stats, errs
}

// Stats returns a copy of the per-stream ledger, in stream insertion order.
func (r *Reorderer) Stats() []Stats {
	r.mu.Lock()
	defer r.mu.Unlock()
	out := make([]Stats, 0, len(r.order))
	for _, id := range r.order {
		out = append(out, r.streams[id].stats)
	}
	return out
}
