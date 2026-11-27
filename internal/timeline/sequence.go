// Package timeline turns per-track RTP packet streams into gap-aware,
// integer-nanosecond timelines and validates cross-track dependency graphs.
package timeline

import (
	"sort"

	"mediad/internal/ingest"
	"mediad/internal/model"
)

// Gap marks a missing extended sequence range. No audio is ever synthesized
// for a gap: the manifest only records where samples are absent.
type Gap struct {
	FromSeq uint64 `json:"from_seq"`
	ToSeq   uint64 `json:"to_seq"`
}

// ReorderResult is the deterministic outcome of feeding one packet.
type ReorderResult struct {
	// Flushed are packets released in extended-sequence order.
	Flushed []*ingest.AudioPacket
	// Gap is non-nil when missing sequence numbers were skipped.
	Gap *Gap
	// Duplicate is true when the packet's sequence was already accepted.
	Duplicate bool
	// Late is non-nil when the packet fell outside the reorder window.
	Late *model.Error
}

// Reorderer buffers out-of-order packets within a fixed sequence window,
// deduplicates repeats, expands 16-bit rollovers and records gaps instead of
// inventing audio. Sequence expansion happens here so every caller uses the
// same single high-water mark.
type Reorderer struct {
	track   string
	window  uint64
	maxSeq  uint64
	started bool
	nextSeq uint64
	pending map[uint64]*ingest.AudioPacket
	seen    map[uint64]bool

	Accepted   uint64
	Duplicates uint64
	Gaps       []Gap
}

// NewReorderer creates a Reorderer. window is the maximum out-of-order span;
// packets more than window sequences behind the high-water mark are rejected
// with ERR_SEQ_LATE.
func NewReorderer(track string, window uint64) *Reorderer {
	if window == 0 {
		window = 64
	}
	return &Reorderer{
		track:   track,
		window:  window,
		pending: make(map[uint64]*ingest.AudioPacket),
		seen:    make(map[uint64]bool),
	}
}

// HighSeq exposes the current extended high-water mark.
func (r *Reorderer) HighSeq() uint64 { return r.maxSeq }

// Push accepts one parsed packet. The 16-bit RTP sequence is expanded against
// this reorderer's high-water mark (RFC 3550 modulo 2^16 semantics).
func (r *Reorderer) Push(pkt *ingest.AudioPacket) ReorderResult {
	ext := ingest.ExtendSequence(pkt.Sequence, r.maxSeq)
	pkt.ExtSequence = ext
	if !r.started {
		r.started = true
		r.nextSeq = ext
	}
	if ext > r.maxSeq {
		r.maxSeq = ext
	}
	if r.maxSeq-ext > r.window {
		return ReorderResult{Late: model.ErrSeqLate.At(r.track, uint32(pkt.Sequence),
			"sequence", pkt.Offset, "packet outside reorder window")}
	}
	if r.seen[ext] {
		r.Duplicates++
		return ReorderResult{Duplicate: true}
	}
	r.seen[ext] = true
	r.Accepted++
	r.pending[ext] = pkt
	return r.drain()
}

// drain releases every contiguous run starting at nextSeq.
func (r *Reorderer) drain() ReorderResult {
	var out ReorderResult
	for {
		pkt, ok := r.pending[r.nextSeq]
		if !ok {
			return out
		}
		out.Flushed = append(out.Flushed, pkt)
		delete(r.pending, r.nextSeq)
		r.nextSeq++
	}
}

// Flush forces out all buffered packets at end of stream. Holes not already
// reported by Push are returned as new gaps; packets come out in
// extended-sequence order.
func (r *Reorderer) Flush() ([]*ingest.AudioPacket, []Gap) {
	keys := r.sortedPending()
	var out []*ingest.AudioPacket
	var gaps []Gap
	cursor := r.nextSeq
	for _, k := range keys {
		if k > cursor {
			g := Gap{FromSeq: cursor, ToSeq: k - 1}
			gaps = append(gaps, g)
			r.Gaps = append(r.Gaps, g)
		}
		out = append(out, r.pending[k])
		cursor = k + 1
	}
	r.pending = map[uint64]*ingest.AudioPacket{}
	r.nextSeq = cursor
	return out, gaps
}

func (r *Reorderer) sortedPending() []uint64 {
	keys := make([]uint64, 0, len(r.pending))
	for k := range r.pending {
		keys = append(keys, k)
	}
	sort.Slice(keys, func(i, j int) bool { return keys[i] < keys[j] })
	return keys
}

// Timeline is the aligned, gap-aware view of one track. All times are integer
// nanoseconds; the native time base is retained for reporting.
type Timeline struct {
	Track      string
	ClockRate  uint32
	StartNS    model.Nano
	EndNS      model.Nano
	Frames     []Frame
	Gaps       []Gap
	Duplicates uint64
	TotalDurNS model.Nano
}

// Frame is one accepted audio frame on the timeline.
type Frame struct {
	ExtSequence uint64
	RTPTime     uint32
	StartNS     model.Nano
	DurationNS  model.Nano
	Bytes       int
}

// BuildTimeline lays flushed packets end-to-end on a zero-origin nanosecond
// axis. Gaps leave holes: no frames are fabricated, and total duration
// therefore excludes missing audio.
func BuildTimeline(spec ingest.TrackSpec, packets []*ingest.AudioPacket, gaps []Gap, duplicates uint64) (*Timeline, error) {
	tl := &Timeline{Track: spec.ID, ClockRate: spec.ClockRate, Gaps: gaps, Duplicates: duplicates}
	var cursor model.Nano
	for _, pkt := range packets {
		start, err := model.ToNano(int64(pkt.RTPTime), spec.ClockRate)
		if err != nil {
			return nil, err
		}
		if len(tl.Frames) == 0 {
			tl.StartNS = start
			cursor = start
		}
		tl.Frames = append(tl.Frames, Frame{
			ExtSequence: pkt.ExtSequence,
			RTPTime:     pkt.RTPTime,
			StartNS:     cursor,
			DurationNS:  pkt.DurationNS,
			Bytes:       len(pkt.Payload),
		})
		cursor, err = model.AddNano(cursor, pkt.DurationNS)
		if err != nil {
			return nil, err
		}
		tl.TotalDurNS, err = model.AddNano(tl.TotalDurNS, pkt.DurationNS)
		if err != nil {
			return nil, err
		}
	}
	if len(tl.Frames) > 0 {
		tl.EndNS = cursor
	}
	return tl, nil
}
