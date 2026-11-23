// Package segment decides replay-index cut points by jointly honoring silence
// boundaries, a maximum segment duration and video keyframe constraints.
package segment

import (
	"mediapipe/internal/audio"
	"mediapipe/internal/fault"
	"mediapipe/internal/model"
)

// Config controls cutting.
type Config struct {
	// MaxSegmentNanos is the hard upper bound on a segment length.
	MaxSegmentNanos int64
	// PreferSilenceNanos pulls a max-duration cut toward nearby silence.
	PreferSilenceNanos int64
}

// DefaultConfig returns the demo cutting policy.
func DefaultConfig() Config {
	return Config{
		MaxSegmentNanos:   1_000_000_000, // 1 s
		PreferSilenceNanos: 150_000_000,  // look +/-150 ms for a silence edge
	}
}

// Segment is one cut with its actual covered range on the session axis.
type Segment struct {
	Index       int   `json:"index"`
	StartNanos  int64 `json:"start_ns"`
	EndNanos    int64 `json:"end_ns"`
	StartSample int   `json:"start_sample"`
	EndSample   int   `json:"end_sample"` // exclusive
	KeyframeCut bool  `json:"keyframe_cut"`
	SilenceCut  bool  `json:"silence_cut"`
}

// Cutter operates on a single video track plus optional silence intervals.
type Cutter struct {
	video *model.Track
	cfg   Config
}

// New builds a Cutter. A nil video track is rejected.
func New(video *model.Track, cfg Config) (*Cutter, *fault.Error) {
	if video == nil || len(video.Samples) == 0 {
		return nil, fault.New(fault.KindInvalidInput,
			"segment cutter requires a non-empty video track",
			"", -1, false)
	}
	return &Cutter{video: video, cfg: cfg}, nil
}

// Cut returns deterministic segments covering the whole video track. Every
// boundary is snapped to a keyframe; a silence edge within the preference
// window is used when available. Coverage ranges are reported explicitly.
func (c *Cutter) Cut(silences []audio.Interval, errs *fault.Batch) []Segment {
	samples := c.video.Samples
	n := len(samples)
	sessionPTS := func(i int) int64 { return samples[i].PTS + c.video.OffsetNanos }
	var segs []Segment
	start := 0
	idx := 0
	for start < n {
		startTime := sessionPTS(start)
		// Hard limit index: first sample at or beyond max duration.
		hardEnd := start + 1
		for hardEnd < n && sessionPTS(hardEnd)-startTime < c.cfg.MaxSegmentNanos {
			hardEnd++
		}
		end := hardEnd
		cut := c.pickCut(start, hardEnd, n, sessionPTS, silences)
		end = cut.index
		if end <= start {
			end = hardEnd
		}
		seg := Segment{
			Index: idx, StartNanos: sessionPTS(start),
			EndNanos: sessionPTS(end-1) + samples[end-1].DurationNanos,
			StartSample: start, EndSample: end,
			KeyframeCut: samples[start].Keyframe,
			SilenceCut:  cut.silence,
		}
		if seg.EndNanos < seg.StartNanos {
			errs.Add(fault.New(fault.KindInvariant,
				"negative segment duration", c.video.SourceFile,
				samples[end-1].PayloadOff, false))
			seg.EndNanos = seg.StartNanos
		}
		segs = append(segs, seg)
		idx++
		start = end
	}
	return segs
}

type cutChoice struct {
	index   int
	silence bool
}

// pickCut chooses a boundary: prefer a keyframe nearest a silence edge, then
// the nearest keyframe to the hard limit, then the hard limit itself.
func (c *Cutter) pickCut(start, hardEnd, n int,
	sessionPTS func(int) int64, silences []audio.Interval) cutChoice {
	samples := c.video.Samples
	limitTime := sessionPTS(hardEnd - 1)
	// 1. A silence edge inside the preference window and on/after a keyframe.
	bestSilence := -1
	bestSilenceDist := int64(1<<62)
	for _, iv := range silences {
		for _, edge := range []int64{iv.StartNanos, iv.EndNanos} {
			d := edge - limitTime
			if d < 0 {
				d = -d
			}
			if d > c.cfg.PreferSilenceNanos {
				continue
			}
			// Find the keyframe at or just before this edge within [start,hardEnd).
			for i := hardEnd - 1; i > start; i-- {
				if samples[i].Keyframe && d <= bestSilenceDist {
					bestSilence, bestSilenceDist = i, d
					break
				}
			}
		}
	}
	if bestSilence > start {
		return cutChoice{index: bestSilence, silence: true}
	}
	// 2. Nearest keyframe to the hard limit.
	for i := hardEnd - 1; i > start; i-- {
		if samples[i].Keyframe {
			return cutChoice{index: i}
		}
	}
	// 3. Hard limit; keyframe constraint cannot be met so emit a fault but
	// keep the segment rather than dropping coverage.
	return cutChoice{index: hardEnd}
}
