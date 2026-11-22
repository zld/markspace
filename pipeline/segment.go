// Package pipeline consumes a verified index and derives segments, silence
// intervals and content-fingerprint duplicate groups using a single media
// clock and error model.
package pipeline

import (
	"fmt"

	"mediacore/index"
	"mediacore/media"
)

// Segment is one contiguous run of frames on the media timeline.
type Segment struct {
	Index      int    `json:"index"`
	StartTicks int64  `json:"start_ticks"`
	EndTicks   int64  `json:"end_ticks"`
	StartMS    int64  `json:"start_ms"`
	EndMS      int64  `json:"end_ms"`
	FirstSeq   uint32 `json:"first_seq"`
	LastSeq    uint32 `json:"last_seq"`
	FrameCount int    `json:"frame_count"`
	// GapCount counts missing-frame runs wholly or partly inside this segment.
	GapCount int `json:"gap_count"`
	// MissingFrames is the number of absent frame slots inside the segment.
	MissingFrames int `json:"missing_frames"`
	// ClosedBy is "duration", "gap", "regression", "clock_wrap" or "end".
	ClosedBy string `json:"closed_by"`
	Bytes    int64  `json:"bytes"`
}

// SegmentConfig controls boundary policy. All thresholds are in ticks of the
// shared media clock so every stage agrees on time.
type SegmentConfig struct {
	// MaxDurationTicks forces a boundary at or beyond this span.
	MaxDurationTicks int64
	// MaxGapTicks is the largest hole between consecutive frames before a
	// boundary is cut. Zero disables gap-based cuts.
	MaxGapTicks int64
	// BoundaryToleranceTicks allows a frame this close to the maximum duration
	// to remain in the current segment instead of spilling one frame over.
	BoundaryToleranceTicks int64
	// Timescale is the clock rate, carried for millisecond reporting.
	Timescale uint32
	// GapTicks is how far missing sequence numbers are expected to advance the
	// clock; used to attribute holes to segments when frames are absent.
	GapTicks int64
}

// DefaultSegmentConfig yields 4 s segments with a 400 ms gap threshold at 90 kHz.
func DefaultSegmentConfig(timescale uint32) SegmentConfig {
	clk := media.MustClock(timescale)
	return SegmentConfig{
		MaxDurationTicks:       clk.FromMS(4000),
		MaxGapTicks:            clk.FromMS(400),
		BoundaryToleranceTicks: clk.FromMS(2),
		Timescale:              timescale,
		GapTicks:               clk.FromMS(40),
	}
}

// Validate checks the segment policy.
func (c SegmentConfig) Validate() error {
	if c.Timescale == 0 {
		return media.New(media.CatInvalidParams, media.StageSegment, "timescale must be positive")
	}
	if c.MaxDurationTicks <= 0 {
		return media.New(media.CatInvalidParams, media.StageSegment, "max duration must be positive")
	}
	if c.MaxGapTicks < 0 || c.BoundaryToleranceTicks < 0 || c.GapTicks < 0 {
		return media.New(media.CatInvalidParams, media.StageSegment, "segment thresholds cannot be negative")
	}
	return nil
}

// GapRef locates a missing-frame run on the index timeline.
type GapRef struct {
	PrevSeq   uint32 `json:"prev_seq"`
	NextSeq   uint32 `json:"next_seq"`
	Missing   int    `json:"missing"`
	AfterMS   int64  `json:"after_ms"`
	SpanTicks int64  `json:"span_ticks"`
}

// Segmenter turns an index into segments and can report the boundary policy
// decision made at each cut.
type Segmenter struct {
	cfg SegmentConfig
}

// NewSegmenter validates cfg and returns a segmenter.
func NewSegmenter(cfg SegmentConfig) (*Segmenter, error) {
	if err := cfg.Validate(); err != nil {
		return nil, err
	}
	return &Segmenter{cfg: cfg}, nil
}

type span struct {
	start, end  int64 // media ticks covered by the segment
	first, last uint32
	count       int
	gaps        int
	missing     int
	bytes       int64
	prevTicks   int64
	prevSeq     uint32
}

// Segments partitions entries by media time. A boundary is cut when the span
// would exceed MaxDurationTicks (outside tolerance), when the clock moved
// backwards beyond tolerance, or when the gap from the previous frame exceeds
// MaxGapTicks. Every boundary is attributed to an explicit reason.
func (s *Segmenter) Segments(entries []index.Entry) ([]Segment, []GapRef) {
	segments := make([]Segment, 0, len(entries)/8+1)
	var gaps []GapRef
	var cur *span
	flush := func(reason string) {
		if cur == nil {
			return
		}
		clk := media.MustClock(s.cfg.Timescale)
		segments = append(segments, Segment{
			Index:         len(segments),
			StartTicks:    cur.start,
			EndTicks:      cur.end,
			StartMS:       clk.ToMS(cur.start),
			EndMS:         clk.ToMS(cur.end),
			FirstSeq:      cur.first,
			LastSeq:       cur.last,
			FrameCount:    cur.count,
			GapCount:      cur.gaps,
			MissingFrames: cur.missing,
			ClosedBy:      reason,
			Bytes:         cur.bytes,
		})
		cur = nil
	}
	for i := range entries {
		e := entries[i]
		if e.Regressed {
			flush("regression")
		}
		entryEnd := e.Ticks
		if e.DurationTicks > 0 {
			entryEnd += int64(e.DurationTicks)
		}
		if cur != nil {
			spanTicks := entryEnd - cur.start
			tooLong := spanTicks > s.cfg.MaxDurationTicks+s.cfg.BoundaryToleranceTicks
			var hole int64
			if s.cfg.GapTicks > 0 && e.Ticks > cur.prevTicks {
				hole = e.Ticks - cur.prevTicks
			}
			if hole > s.cfg.MaxGapTicks && s.cfg.MaxGapTicks > 0 {
				missing := countMissing(cur.prevSeq, e.Seq)
				gaps = append(gaps, GapRef{
					PrevSeq:   cur.prevSeq,
					NextSeq:   e.Seq,
					Missing:   missing,
					AfterMS:   media.MustClock(s.cfg.Timescale).ToMS(cur.prevTicks),
					SpanTicks: hole,
				})
				// The hole belongs to the segment being closed: it is the run
				// of frames that should have extended it.
				cur.gaps++
				cur.missing += missing
				flush("gap")
			} else if tooLong {
				flush("duration")
			} else if missing := countMissing(cur.prevSeq, e.Seq); missing > 0 {
				cur.gaps++
				cur.missing += missing
			}
		}
		if cur == nil {
			cur = &span{start: e.Ticks, first: e.Seq}
		}
		cur.last = e.Seq
		cur.count++
		// Bytes are the full encoded record size: this is what the capture
		// path actually moved and what the bitrate target applies to.
		cur.bytes += int64(e.Length)
		cur.end = entryEnd
		cur.prevTicks = e.Ticks
		cur.prevSeq = e.Seq
	}
	flush("end")
	return segments, gaps
}

// countMissing returns how many sequence numbers fall strictly between prev
// and next, honoring 16-bit wraparound. A backward step (next behind prev, as
// happens for reordered or wrapped frames on a sorted timeline) is not a gap,
// so it counts zero rather than a full wraparound's worth of missing frames.
func countMissing(prev, next uint32) int {
	d := int16(next - prev)
	if d <= 0 {
		return 0
	}
	return int(d) - 1
}

// Config exposes the active segment policy.
func (s *Segmenter) Config() SegmentConfig { return s.cfg }

// Describe renders a segment for terminal output.
func (s Segment) Describe() string {
	return fmt.Sprintf("segment=%d frames=%d ms=%d..%d gaps=%d missing=%d closed_by=%s",
		s.Index, s.FrameCount, s.StartMS, s.EndMS, s.GapCount, s.MissingFrames, s.ClosedBy)
}
