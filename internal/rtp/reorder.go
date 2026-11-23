package rtp

import (
	"sync"

	"mediaverify/internal/clock"
	"mediaverify/internal/errs"
)

// MaxCacheBytes is the hard cap on reorder-buffer payload memory (64 MiB).
const MaxCacheBytes = 64 * 1024 * 1024

// Stats summarizes what the reorder buffer observed for one stream.
type Stats struct {
	Received   int // packets offered (excluding parse failures)
	Duplicate  int // repeated (SSRC, ext-seq) packets, counted but dropped
	Reordered  int // valid packets that arrived after a later sequence
	Lost       int // sequence numbers never seen once the window moved on
	Gaps       int // contiguous loss runs reported as gaps
	CacheBytes int // current buffered payload bytes
}

// Gap is a contiguous run of missing sequence numbers.
type Gap struct {
	SSRC   uint32
	Start  uint64
	Length int
}

// ReorderBuffer deduplicates and orders packets per SSRC. Packets outside
// the reorder window are treated as permanent gaps; late packets landing in
// a sealed region are rejected (the timeline layer decides on replay).
type ReorderBuffer struct {
	mu       sync.Mutex
	window   uint64
	maxBytes int
	streams  map[uint32]*streamState
	Stats    map[uint32]*Stats
	Gaps     []Gap
}

type streamState struct {
	nextExpected uint64 // next in-order sequence to emit
	started      bool
	pending      map[uint64]Packet
	bytes        int
}

// NewReorderBuffer builds a buffer with the given window size in packets.
func NewReorderBuffer(window uint64) *ReorderBuffer {
	if window == 0 {
		window = 64
	}
	return &ReorderBuffer{
		window:   window,
		maxBytes: MaxCacheBytes,
		streams:  map[uint32]*streamState{},
		Stats:    map[uint32]*Stats{},
	}
}

func (b *ReorderBuffer) state(ssrc uint32) *streamState {
	s := b.streams[ssrc]
	if s == nil {
		s = &streamState{pending: map[uint64]Packet{}}
		b.streams[ssrc] = s
		b.Stats[ssrc] = &Stats{}
	}
	return s
}

// OfferResult tells the caller what happened to an offered packet.
type OfferResult struct {
	Accepted  bool
	Duplicate bool
	Sealed    bool
	Emitted   []Packet // in-order packets released by this offer
	NewGaps   []Gap
}

// Offer inserts one packet. The returned slice of packets is in sequence
// order; the buffer never emits duplicates.
func (b *ReorderBuffer) Offer(p Packet) (OfferResult, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	s := b.state(p.SSRC)
	st := b.Stats[p.SSRC]
	st.Received++
	res := OfferResult{}

	if !s.started {
		s.started = true
		s.nextExpected = p.ExtSeq
	}
	// Already emitted: the interval is sealed.
	if p.ExtSeq < s.nextExpected {
		st.Duplicate++
		res.Duplicate = true
		res.Sealed = true
		return res, nil
	}
	if _, exists := s.pending[p.ExtSeq]; exists {
		st.Duplicate++
		res.Duplicate = true
		return res, nil
	}
	// The memory budget only constrains packets that will stay buffered
	// waiting for earlier sequences; the in-order packet releases at once.
	if p.ExtSeq != s.nextExpected && s.bytes+len(p.Payload) > b.maxBytes {
		return res, errs.New(errs.KindLimitExceeded, "rtp",
			"reorder cache exceeds %d bytes for ssrc=%d", b.maxBytes, p.SSRC)
	}
	if p.ExtSeq > s.nextExpected {
		st.Reordered++
	}
	s.pending[p.ExtSeq] = p
	s.bytes += len(p.Payload)
	st.CacheBytes = s.bytes
	res.Accepted = true

	// Release everything contiguous from nextExpected.
	for {
		q, ok := s.pending[s.nextExpected]
		if !ok {
			break
		}
		res.Emitted = append(res.Emitted, q)
		delete(s.pending, s.nextExpected)
		s.bytes -= len(q.Payload)
		s.nextExpected++
	}
	st.CacheBytes = s.bytes

	// Anything that falls behind the window is a permanent gap.
	gaps := b.flushExpired(s, p.SSRC)
	res.NewGaps = gaps
	return res, nil
}

// flushExpired marks sequences older than (highest buffered - window) as
// lost and removes them from consideration.
func (b *ReorderBuffer) flushExpired(s *streamState, ssrc uint32) []Gap {
	var highest uint64
	for seq := range s.pending {
		if seq > highest {
			highest = seq
		}
	}
	if highest <= s.nextExpected+b.window {
		return nil
	}
	limit := highest - b.window
	var out []Gap
	var runStart uint64
	runLen := 0
	flush := func(seq uint64) {
		if runLen == 0 {
			runStart = seq
		}
		runLen++
	}
	for seq := s.nextExpected; seq < limit; seq++ {
		if _, ok := s.pending[seq]; ok {
			continue
		}
		flush(seq)
	}
	if runLen > 0 {
		g := Gap{SSRC: ssrc, Start: runStart, Length: runLen}
		out = append(out, g)
		// Merge with the previous run when the loss is contiguous even
		// though it was discovered on different offers.
		if n := len(b.Gaps); n > 0 {
			prev := &b.Gaps[n-1]
			if prev.SSRC == ssrc && prev.Start+uint64(prev.Length) == runStart {
				prev.Length += runLen
			} else {
				b.Gaps = append(b.Gaps, g)
				b.Stats[ssrc].Gaps++
			}
		} else {
			b.Gaps = append(b.Gaps, g)
			b.Stats[ssrc].Gaps++
		}
		b.Stats[ssrc].Lost += runLen
		s.nextExpected = limit
	}
	return out
}

// Drain emits all currently contiguous packets (used at stream end).
func (b *ReorderBuffer) Drain(ssrc uint32) []Packet {
	b.mu.Lock()
	defer b.mu.Unlock()
	s := b.streams[ssrc]
	if s == nil {
		return nil
	}
	var out []Packet
	for {
		q, ok := s.pending[s.nextExpected]
		if !ok {
			break
		}
		out = append(out, q)
		delete(s.pending, s.nextExpected)
		s.bytes -= len(q.Payload)
		s.nextExpected++
	}
	return out
}

// Window returns the configured reorder window.
func (b *ReorderBuffer) Window() uint64 { return b.window }

// SnapshotStats returns a copy of per-SSRC stats.
func (b *ReorderBuffer) SnapshotStats() map[uint32]Stats {
	b.mu.Lock()
	defer b.mu.Unlock()
	out := make(map[uint32]Stats, len(b.Stats))
	for k, v := range b.Stats {
		out[k] = *v
	}
	return out
}

// ClockStamp converts an RTP timestamp to shared ticks.
func ClockStamp(ts uint32, base uint32, baseTicks clock.Ticks, rate clock.Rational) (clock.Ticks, error) {
	delta := int64(ts) - int64(base) // signed: handles one 32-bit wrap span
	dt, err := clock.ScaleToTicks(delta, rate)
	if err != nil {
		return 0, err
	}
	return baseTicks + dt, nil
}
