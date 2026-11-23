// Package segment builds the deterministic shard manifest and per-segment
// SHA-256 digests from indexed blocks.
package segment

import (
	"errors"
	"fmt"
	"sort"

	"mediad/internal/ebml"
	"mediad/internal/index"
)

// Config identifies the processing parameters that affect segment output.
// Two runs with different configs must not share cache entries.
type Config struct {
	Representations int    `json:"representations"`
	TargetDuration  int64  `json:"target_duration_ns"`
	Tolerance       int64  `json:"tolerance_ns"`
	TimecodeScale   uint64 `json:"timecode_scale"`
	FrameNanos      int64  `json:"frame_nanos"`
}

// Digest returns a stable SHA-256 of the config fields.
func (c Config) Digest() string { return digestConfig(c) }

// BlockRef records a block's contribution to a segment with provenance.
type BlockRef struct {
	Track    uint64    `json:"track"`
	Timecode int64     `json:"timecode"`
	TimeNS   int64     `json:"time_ns"`
	Keyframe bool      `json:"keyframe"`
	Source   ebml.Span `json:"source"`
}

// AudioSpan assigns audio blocks to a segment by time interval overlap.
type AudioSpan struct {
	Track  uint64 `json:"track"`
	Start  int64  `json:"start_ns"`
	End    int64  `json:"end_ns"`
	Blocks int    `json:"blocks"`
}

// Segment is one entry in the manifest.
type Segment struct {
	Index       int         `json:"index"`
	StartNS     int64       `json:"start_ns"`
	EndNS       int64       `json:"end_ns"`
	DurationNS  int64       `json:"duration_ns"`
	VideoTrack  uint64      `json:"video_track"`
	VideoBlocks int         `json:"video_blocks"`
	AudioBlocks int         `json:"audio_blocks"`
	Keyframe    ebml.Span   `json:"keyframe_source"`
	KeyframeNS  int64       `json:"keyframe_ns"`
	Audio       []AudioSpan `json:"audio_spans,omitempty"`
	SourceStart int64       `json:"source_start"`
	SourceEnd   int64       `json:"source_end"`
	Hash        string      `json:"sha256"`
}

// Manifest is the deterministic segment list for one representation.
type Manifest struct {
	Version        int       `json:"version"`
	Representation string    `json:"representation"`
	Config         Config    `json:"config"`
	ConfigDigest   string    `json:"config_digest"`
	InputDigest    string    `json:"input_digest"`
	Segments       []Segment `json:"segments"`
}

// BuildInput describes what to segment.
type BuildInput struct {
	Representation string
	VideoTrack     uint64
	AudioTracks    []uint64
	Index          *index.Index
	Cuts           []int64
	Config         Config
	InputDigest    string
	// Payload hashes are caller-supplied per source span so the digest covers
	// real bytes without the segmenter reading the input again.
	HashSpan func(ebml.Span) ([32]byte, error)
}

// ErrNoSegments reports an empty cut grid.
var ErrNoSegments = errors.New("segment: no cuts to build")

// Build produces the manifest and per-segment hashes. Segment boundaries are
// half-open: a block at exactly the cut timestamp belongs to the later
// segment, so no frame is counted twice or dropped.
func Build(in BuildInput) (*Manifest, error) {
	if len(in.Cuts) == 0 {
		return nil, ErrNoSegments
	}
	if len(in.Cuts) == 1 {
		// A single cut still yields one segment covering everything after it.
		in.Cuts = append(in.Cuts, maxInt64)
	}
	videoTrack := in.VideoTrack
	video := in.Index.Entries(videoTrack)
	audioByTrack := make(map[uint64][]index.Entry, len(in.AudioTracks))
	for _, t := range in.AudioTracks {
		audioByTrack[t] = in.Index.Entries(t)
	}
	m := &Manifest{
		Version:        1,
		Representation: in.Representation,
		Config:         in.Config,
		ConfigDigest:   in.Config.Digest(),
		InputDigest:    in.InputDigest,
		Segments:       make([]Segment, 0, len(in.Cuts)-1),
	}
	for i := 0; i+1 < len(in.Cuts); i++ {
		start, end := in.Cuts[i], in.Cuts[i+1]
		seg := Segment{Index: i, StartNS: start, EndNS: end, VideoTrack: videoTrack}
		if end != maxInt64 {
			seg.DurationNS = end - start
		}
		// Video: keyframes in [start, end).
		kf, _, ok := in.Index.AtOrBefore(videoTrack, start)
		if !ok {
			return nil, fmt.Errorf("segment: no keyframe at or before cut %d ns", start)
		}
		seg.Keyframe = kf.Source
		seg.KeyframeNS = kf.TimeNS
		for _, e := range video {
			if e.TimeNS >= start && e.TimeNS < end {
				seg.VideoBlocks++
				seg.SourceStart, seg.SourceEnd = extend(seg.SourceStart, seg.SourceEnd, e.Source)
			}
		}
		for _, at := range in.AudioTracks {
			sp := AudioSpan{Track: at, Start: start, End: end}
			for _, e := range audioByTrack[at] {
				if e.TimeNS >= start && e.TimeNS < end {
					sp.Blocks++
					seg.AudioBlocks++
					seg.SourceStart, seg.SourceEnd = extend(seg.SourceStart, seg.SourceEnd, e.Source)
				}
			}
			if sp.Blocks > 0 {
				seg.Audio = append(seg.Audio, sp)
			}
		}
		sort.Slice(seg.Audio, func(a, b int) bool { return seg.Audio[a].Track < seg.Audio[b].Track })
		if in.HashSpan != nil {
			h, err := HashSegment(in.HashSpan, seg.SourceStart, seg.SourceEnd)
			if err != nil {
				return nil, err
			}
			seg.Hash = h
		}
		// A final open segment keeps the manifest's covered interval exactly
		// equal to the cut grid, so the verifier can assert no gaps.
		if end == maxInt64 {
			seg.EndNS = seg.SourceEnd
			seg.DurationNS = seg.SourceEnd - seg.StartNS
		}
		m.Segments = append(m.Segments, seg)
	}
	return m, nil
}

func extend(start, end int64, s ebml.Span) (int64, int64) {
	if s.Length == 0 {
		return start, end
	}
	if start == 0 && end == 0 {
		return s.Offset, s.End()
	}
	if s.Offset < start {
		start = s.Offset
	}
	if s.End() > end {
		end = s.End()
	}
	return start, end
}

const maxInt64 = int64(^uint64(0) >> 1)
