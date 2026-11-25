// Package rtp accepts a bounded RTP input stream, de-duplicates it by sequence
// number and delivers packets in sequence order. A leaky frontier (the
// watermark) is the highest sequence seen minus a configured late window:
// holes behind the watermark are declared lost exactly once, and a packet that
// fills one of those holes is routed to the late queue rather than silently
// overwriting the loss record.
package rtp

import (
	"fmt"

	"archive.example/mediakernel/internal/events"

	pionrtp "github.com/pion/rtp"
)

const (
	// SeqModulus is the RTP 16-bit sequence space.
	SeqModulus = 1 << 16
	// HalfSeqSpace is the ambiguity horizon for an unwrapped sequence.
	HalfSeqSpace = SeqModulus / 2

	// ReasonWatermark marks a hole that fell behind the watermark.
	ReasonWatermark = "watermark_passed"
	// ReasonStreamEnd marks a hole still open when the stream ended.
	ReasonStreamEnd = "stream_end"
)

// Packet is one RTP packet reduced to what the archive kernel needs. ExtSeq is
// the unwrapped sequence ordinal used for ordering; Seq is the raw wire value
// used as the stable logical identity in manifests and replays.
type Packet struct {
	Seq         uint32
	ExtSeq      int64
	Timestamp   uint32
	SSRC        uint32
	PayloadType uint8
	Marker      bool
	Payload     []byte
	Input       int
	ByteOffset  int64 // -1 when the packet did not come from a byte stream
}

// Config bounds the reorderer. All limits are enforced with explicit errors.
type Config struct {
	Track      events.TrackID
	LateWindow int64 // packets tolerated behind the highest sequence seen
	MaxSpan    int64 // unwrapped distance from the baseline; default HalfSeqSpace-1
	MaxBuffer  int   // concurrently buffered out-of-order packets; default 4096
	Warmup     int   // packets observed before the baseline is fixed; default 8
}

// PushResult is the deterministic outcome of accepting one packet.
type PushResult struct {
	Deliver    []Packet     // newly deliverable packets, ascending ExtSeq
	Lost       []events.Gap // holes that crossed the watermark on this push
	Late       []Packet     // packets arriving for already-declared holes
	Duplicate  bool         // input was a repeat and must not be counted again
	OutOfOrder bool         // input was inserted behind the frontier
}

// Stats is cumulative reorderer bookkeeping (diagnostic only).
type Stats struct {
	Received   int
	Delivered  int
	Lost       int
	Late       int
	Duplicate  int
	OutOfOrder int
	Watermark  int64
	MaxExt     int64
	HasData    bool
}

// Reorderer deduplicates and resequences one RTP flow.
type Reorderer struct {
	cfg Config

	anchorRaw uint32
	hasAnchor bool
	expected  int64
	maxExt    int64
	watermark int64
	frozen    bool

	warmup    map[int64]Packet
	warmupMax int64
	warmupEnd bool

	buffer   map[int64]Packet
	declared map[int64]bool // holes declared lost, keyed by ExtSeq

	stats Stats
}

// New validates cfg and constructs a reorderer.
func New(cfg Config) (*Reorderer, *events.Error) {
	if cfg.Track == "" {
		cfg.Track = events.TrackRTP
	}
	if cfg.LateWindow < 0 {
		return nil, events.NewError(events.CodeInternal, cfg.Track, 0, -1, "rtp", "negative late window", nil)
	}
	if cfg.MaxSpan <= 0 {
		cfg.MaxSpan = HalfSeqSpace - 1
	}
	if cfg.MaxSpan > HalfSeqSpace-1 {
		return nil, events.NewError(events.CodeInternal, cfg.Track, 0, -1, "rtp",
			fmt.Sprintf("max span %d exceeds unambiguous horizon %d", cfg.MaxSpan, HalfSeqSpace-1), nil)
	}
	if cfg.LateWindow >= cfg.MaxSpan {
		return nil, events.NewError(events.CodeInternal, cfg.Track, 0, -1, "rtp",
			"late window must be smaller than max span", nil)
	}
	if cfg.MaxBuffer <= 0 {
		cfg.MaxBuffer = 4096
	}
	if cfg.Warmup <= 0 {
		cfg.Warmup = 8
	}
	return &Reorderer{
		cfg:      cfg,
		warmup:   make(map[int64]Packet, cfg.Warmup),
		buffer:   make(map[int64]Packet, 64),
		declared: make(map[int64]bool, 64),
	}, nil
}

// Config returns the effective configuration.
func (r *Reorderer) Config() Config { return r.cfg }

// PushRaw parses one wire packet with pion/rtp and accepts it. byteOffset is
// the packet offset in the input byte stream and is carried into every error.
func (r *Reorderer) PushRaw(raw []byte, byteOffset int64) (PushResult, *events.Error) {
	var p pionrtp.Packet
	if err := p.Unmarshal(raw); err != nil {
		return PushResult{}, events.NewError(events.CodeMalformed, r.cfg.Track, 0, byteOffset, "rtp-unmarshal", err.Error(), err)
	}
	pkt := Packet{
		Seq:         uint32(p.SequenceNumber),
		Timestamp:   p.Timestamp,
		SSRC:        p.SSRC,
		PayloadType: p.PayloadType,
		Marker:      p.Marker,
		Payload:     append([]byte(nil), p.Payload...),
		ByteOffset:  byteOffset,
	}
	return r.Push(pkt)
}

// Push accepts one parsed packet and returns what became deliverable.
func (r *Reorderer) Push(pkt Packet) (PushResult, *events.Error) {
	if !r.hasAnchor {
		r.anchorRaw = pkt.Seq % SeqModulus
		r.hasAnchor = true
	}
	ext := r.toExt(pkt.Seq)
	if ext < -r.cfg.MaxSpan || ext > r.cfg.MaxSpan {
		return PushResult{}, events.NewError(events.CodeCapacity, r.cfg.Track, uint64(pkt.Seq), pkt.ByteOffset, "rtp-span",
			fmt.Sprintf("sequence distance %d outside max span %d", ext, r.cfg.MaxSpan), nil)
	}

	p := pkt
	p.ExtSeq = ext

	if !r.frozen {
		if _, ok := r.warmup[ext]; ok {
			r.stats.Duplicate++
			return PushResult{Duplicate: true}, nil
		}
		r.stats.Received++
		p.Input = r.stats.Received - 1
		if r.warmupMax > ext {
			r.stats.OutOfOrder++
		} else {
			r.warmupMax = ext
		}
		r.warmup[ext] = p
		r.stats.HasData = true
		if len(r.warmup) < r.cfg.Warmup {
			return PushResult{}, nil
		}
		return r.freeze()
	}

	return r.accept(p)
}

// accept handles a packet once the baseline is fixed.
func (r *Reorderer) accept(p Packet) (PushResult, *events.Error) {
	if r.declared[p.ExtSeq] {
		r.stats.Received++
		r.stats.Late++
		return PushResult{Late: []Packet{p}}, nil
	}

	// Below expected is already delivered; it can only be a duplicate.
	if p.ExtSeq < r.expected {
		r.stats.Duplicate++
		return PushResult{Duplicate: true}, nil
	}
	if _, ok := r.buffer[p.ExtSeq]; ok {
		r.stats.Duplicate++
		return PushResult{Duplicate: true}, nil
	}
	if len(r.buffer) >= r.cfg.MaxBuffer {
		return PushResult{}, events.NewError(events.CodeCapacity, r.cfg.Track, uint64(p.Seq), p.ByteOffset, "rtp-buffer",
			fmt.Sprintf("buffer limit %d reached", r.cfg.MaxBuffer), nil)
	}

	var res PushResult
	r.stats.Received++
	p.Input = r.stats.Received - 1
	if p.ExtSeq < r.maxExt {
		r.stats.OutOfOrder++
		res.OutOfOrder = true
	}
	r.buffer[p.ExtSeq] = p
	if p.ExtSeq > r.maxExt {
		r.maxExt = p.ExtSeq
	}
	r.updateWatermark()
	r.advance(&res, ReasonWatermark)
	return res, nil
}

// freeze fixes the baseline at the lowest observation seen during warmup and
// replays the warm-up packets through the ordered path. Because the baseline
// is the minimum rather than the first arrival, a reordered opening cannot
// strand earlier packets outside the window.
func (r *Reorderer) freeze() (PushResult, *events.Error) {
	r.frozen = true
	r.warmupEnd = true

	// The warm-up map is keyed by ExtSeq, so ascending keys are canonical.
	keys := make([]int64, 0, len(r.warmup))
	for k := range r.warmup {
		keys = append(keys, k)
	}
	for i := 1; i < len(keys); i++ {
		for j := i; j > 0 && keys[j] < keys[j-1]; j-- {
			keys[j], keys[j-1] = keys[j-1], keys[j]
		}
	}
	r.expected = keys[0]
	r.maxExt = keys[len(keys)-1]
	r.watermark = r.maxExt - r.cfg.LateWindow
	r.stats.Watermark = r.watermark
	r.stats.MaxExt = r.maxExt

	var order []Packet
	for _, k := range keys {
		p := r.warmup[k]
		if len(r.buffer) >= r.cfg.MaxBuffer {
			return PushResult{}, events.NewError(events.CodeCapacity, r.cfg.Track, uint64(p.Seq), p.ByteOffset, "rtp-buffer",
				fmt.Sprintf("buffer limit %d reached", r.cfg.MaxBuffer), nil)
		}
		order = append(order, p)
	}
	r.warmup = make(map[int64]Packet, 0)

	var res PushResult
	for _, p := range order {
		r.buffer[p.ExtSeq] = p
	}
	r.advance(&res, ReasonWatermark)
	return res, nil
}

func (r *Reorderer) updateWatermark() {
	r.watermark = r.maxExt - r.cfg.LateWindow
	r.stats.Watermark = r.watermark
	r.stats.MaxExt = r.maxExt
}

// toExt unwraps a raw 16-bit sequence relative to the baseline packet. It is a
// pure function of (anchorRaw, raw), so one wire value always maps to one
// ordinal regardless of arrival order.
func (r *Reorderer) toExt(raw uint32) int64 {
	base := int64(r.anchorRaw % SeqModulus)
	delta := (int64(raw%SeqModulus) - base + SeqModulus) % SeqModulus
	if delta > HalfSeqSpace {
		delta -= SeqModulus
	}
	return delta
}

// advance delivers everything contiguous from the next expected ordinal and
// converts holes strictly behind the watermark into explicit losses.
func (r *Reorderer) advance(res *PushResult, reason string) {
	for {
		if p, ok := r.buffer[r.expected]; ok {
			delete(r.buffer, r.expected)
			res.Deliver = append(res.Deliver, p)
			r.stats.Delivered++
			r.expected++
			continue
		}
		if r.expected >= r.maxExt || r.expected >= r.watermark {
			return
		}
		r.declareGap(r.expected, reason, res)
		r.expected++
	}
}

func (r *Reorderer) declareGap(ext int64, reason string, res *PushResult) {
	if r.declared[ext] {
		return
	}
	r.declared[ext] = true
	r.stats.Lost++
	res.Lost = append(res.Lost, events.Gap{
		Track:  r.cfg.Track,
		Kind:   events.KindRTP,
		Seq:    uint64(r.rawSeq(ext)),
		At:     -1, // no timestamp is knowable for absent data
		Reason: reason,
	})
}

// rawSeq maps an unwrapped ordinal back to the raw wire sequence used as the
// logical identity in gaps and replayed events.
func (r *Reorderer) rawSeq(ext int64) uint32 {
	return uint32((int64(r.anchorRaw)%SeqModulus + ext) % SeqModulus)
}

// Flush closes the window at end of input. Holes still open become losses and
// buffered packets are delivered in order.
func (r *Reorderer) Flush() PushResult {
	var res PushResult
	if !r.stats.HasData {
		return res
	}
	if !r.frozen {
		// Too short to warm up: freeze the baseline now.
		fr, _ := r.freeze()
		res.Deliver = append(res.Deliver, fr.Deliver...)
		res.Lost = append(res.Lost, fr.Lost...)
	}
	for r.expected < r.maxExt {
		if p, ok := r.buffer[r.expected]; ok {
			delete(r.buffer, r.expected)
			res.Deliver = append(res.Deliver, p)
			r.stats.Delivered++
		} else {
			r.declareGap(r.expected, ReasonStreamEnd, &res)
		}
		r.expected++
	}
	r.buffer = make(map[int64]Packet, 0)
	r.watermark = r.maxExt
	r.stats.Watermark = r.watermark
	return res
}

// Stats returns a snapshot of cumulative counters.
func (r *Reorderer) Stats() Stats { return r.stats }

// Status is a coarse classification used in terminal summaries.
type Status string

const (
	StatusOrdered   Status = "ordered"
	StatusReordered Status = "reordered"
	StatusLossy     Status = "lossy"
)

// Status summarises the flow.
func (r *Reorderer) Status() Status {
	switch {
	case r.stats.Lost > 0 || r.stats.Late > 0:
		return StatusLossy
	case r.stats.OutOfOrder > 0 || r.stats.Duplicate > 0:
		return StatusReordered
	default:
		return StatusOrdered
	}
}

// StatsLines renders counters for terminal output in a fixed order.
func StatsLines(s Stats) []string {
	return []string{
		fmt.Sprintf("received=%d", s.Received),
		fmt.Sprintf("delivered=%d", s.Delivered),
		fmt.Sprintf("duplicates=%d", s.Duplicate),
		fmt.Sprintf("out_of_order=%d", s.OutOfOrder),
		fmt.Sprintf("lost=%d", s.Lost),
		fmt.Sprintf("late=%d", s.Late),
	}
}
