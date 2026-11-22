package pipeline

import (
	"encoding/hex"

	"mediacore/index"
	"mediacore/media"
)

// FrameSource supplies verified frame bytes for an index entry. The pipeline
// only ever asks for entries it holds in the verified index, and re-validates
// each record it reads.
type FrameSource interface {
	// FrameAt decodes and validates the record at entry.Offset and checks it
	// against the index entry.
	FrameAt(e index.Entry) (*media.Frame, error)
}

// BufferSource serves frames from an in-memory validated stream. It refuses
// offsets that do not align with the given index and rejects any record whose
// content changed since the index was built.
type BufferSource struct {
	Data   []byte
	Limits media.Limits
	// Seen records which offsets were consumed, to prove only indexed entries
	// were read.
	Seen map[int64]bool
}

// NewBufferSource wraps a validated stream for the pipeline.
func NewBufferSource(data []byte, limits media.Limits) *BufferSource {
	if limits.MaxPayload == 0 {
		limits = media.DefaultLimits()
	}
	return &BufferSource{Data: data, Limits: limits, Seen: make(map[int64]bool)}
}

// FrameAt decodes and verifies the record at e.Offset.
func (s *BufferSource) FrameAt(e index.Entry) (*media.Frame, error) {
	f, err := media.ParseFrameAt(s.Data, e.Offset, s.Limits)
	if err != nil {
		return nil, err
	}
	if f.Len() != e.Length {
		return nil, media.New(media.CatIndexInput, media.StageSegment,
			"record length %d != indexed %d", f.Len(), e.Length).
			WithOffset(e.Offset).WithSeq(e.Seq)
	}
	if f.Seq != e.Seq {
		return nil, media.New(media.CatIndexInput, media.StageSegment,
			"record seq %d != indexed %d", f.Seq, e.Seq).
			WithOffset(e.Offset).WithSeq(e.Seq)
	}
	// The index recorded each record's own SHA-256. Re-checking it here proves
	// the pipeline is consuming bytes that still match what was verified at
	// scan time, not merely bytes that happen to parse.
	if hex.EncodeToString(f.Digest[:]) != e.Digest {
		return nil, media.New(media.CatIndexInput, media.StageSegment,
			"record content digest no longer matches the index").
			WithOffset(e.Offset).WithSeq(e.Seq)
	}
	if s.Seen != nil {
		s.Seen[e.Offset] = true
	}
	return f, nil
}

// Result is the complete streaming output for one run.
type Result struct {
	Segments           []Segment         `json:"segments"`
	Silence            []SilenceInterval `json:"silence"`
	DuplicateGroups    []DuplicateGroup  `json:"duplicate_groups"`
	FingerprintCount   int               `json:"fingerprint_count"`
	DuplicateFrames    int               `json:"duplicate_frames"`
	Collisions         int               `json:"collisions"`
	FrameCount         int               `json:"frame_count"`
	Bytes              int64             `json:"bytes"`
	Timescale          uint32            `json:"timescale"`
	DurationMS         int64             `json:"duration_ms"`
	SilenceConfirmedMS int64             `json:"silence_confirmed_ms"`
	SilenceUnknownMS   int64             `json:"silence_unknown_ms"`
	// MediaDurationMS covers the full span including the final frame duration.
	MediaDurationMS int64    `json:"media_duration_ms"`
	Gaps            []GapRef `json:"gaps"`
}

// Config bundles every streaming policy.
type Config struct {
	Segment     SegmentConfig
	Silence     SilenceConfig
	Fingerprint FingerprintConfig
}

// DefaultConfig builds a consistent policy set for one timescale.
func DefaultConfig(timescale uint32) Config {
	return Config{
		Segment:     DefaultSegmentConfig(timescale),
		Silence:     DefaultSilenceConfig(timescale),
		Fingerprint: DefaultFingerprintConfig(),
	}
}

// Validate checks all sub-policies agree on one timescale.
func (c Config) Validate() error {
	if err := c.Segment.Validate(); err != nil {
		return err
	}
	if err := c.Silence.Validate(); err != nil {
		return err
	}
	if err := c.Fingerprint.Validate(); err != nil {
		return err
	}
	if c.Segment.Timescale != c.Silence.Timescale {
		return media.New(media.CatInvalidParams, media.StageSegment,
			"segment timescale %d != silence timescale %d", c.Segment.Timescale, c.Silence.Timescale)
	}
	return nil
}

// Run consumes entries in index order exactly once, feeding the same verified
// frames to segmentation, silence detection and fingerprinting.
func Run(idx *index.Index, src FrameSource, cfg Config) (*Result, error) {
	if err := cfg.Validate(); err != nil {
		return nil, err
	}
	if idx == nil || len(idx.Entries) == 0 {
		return &Result{Timescale: cfg.Segment.Timescale}, nil
	}
	seg, err := NewSegmenter(cfg.Segment)
	if err != nil {
		return nil, err
	}
	det, err := NewDetector(cfg.Silence)
	if err != nil {
		return nil, err
	}
	dedup, err := NewDeduper(cfg.Fingerprint)
	if err != nil {
		return nil, err
	}
	for i := range idx.Entries {
		e := idx.Entries[i]
		f, err := src.FrameAt(e)
		if err != nil {
			return nil, err
		}
		if _, _, err := dedup.Add(e, f); err != nil {
			return nil, err
		}
	}
	clk := media.MustClock(cfg.Segment.Timescale)
	segments, gaps := seg.Segments(idx.Entries)
	res := &Result{
		Segments:         segments,
		Silence:          det.Detect(idx.Entries),
		DuplicateGroups:  dedup.DuplicateGroups(),
		FingerprintCount: len(dedup.Groups()),
		DuplicateFrames:  dedup.DuplicateFrameCount(),
		Collisions:       dedup.Collisions(),
		FrameCount:       len(idx.Entries),
		Timescale:        cfg.Segment.Timescale,
		Gaps:             gaps,
	}
	for _, e := range idx.Entries {
		res.Bytes += int64(e.Length)
	}
	res.DurationMS = clk.DurationMS(idx.StartTicks, idx.EndTicks)
	res.MediaDurationMS = clk.DurationMS(idx.StartTicks, idx.EndTicks+int64(idx.Entries[len(idx.Entries)-1].DurationTicks))
	res.SilenceConfirmedMS = TotalMS(res.Silence, SilenceConfirmed)
	res.SilenceUnknownMS = TotalMS(res.Silence, SilenceUnknown)
	return res, nil
}
