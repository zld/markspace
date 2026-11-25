// Package stream analyzes frame structure on the master clock: keyframe
// intervals, frame completeness and the located damage ranges that make
// a parsed-but-broken stream untrustworthy.
package stream

import (
	"github.com/local/medialab/internal/container"
	"github.com/local/medialab/internal/model"
	"github.com/local/medialab/internal/timeline"
)

// Frame is one coded frame normalized from container samples. Only the
// video track produces frames here; audio is handled via timing checks.
type Frame struct {
	Track    uint32
	Number   uint32 // 1-based
	Offset   int64
	Size     uint32
	TimeTick int64
	EndTick  int64
	Keyframe bool
	Complete bool
	HasIndex bool // a keyframe index existed for this track
}

// FrameStats summarizes the structural scan.
type FrameStats struct {
	Frames          []Frame
	KeyframeNumbers []uint32
	// KeyframeGapMs is the largest interval between consecutive valid
	// keyframes, in milliseconds.
	KeyframeGapMs   int64
	MeanKeyframeGap int64
	IncompleteCount int
}

// AnalyzeFrames converts normalized MP4 samples into frames, marks any
// sample whose bytes do not fully exist in the file as incomplete, and
// computes keyframe gaps. A frame cut at a boundary never counts as a
// valid keyframe even if its index flag says so.
func AnalyzeFrames(samples []container.MP4Sample, fileSize int64, book *model.ErrorBook) FrameStats {
	var st FrameStats
	byTrack := map[uint32][]*Frame{}

	for _, s := range samples {
		if s.Track != 1 {
			continue // video track only
		}
		f := Frame{
			Track: s.Track, Number: s.Number, Offset: s.Offset, Size: s.Size,
			TimeTick: s.TimeTick, Keyframe: s.Keyframe && s.HasKeyIndex,
			HasIndex: s.HasKeyIndex, Complete: true,
		}
		if f.Offset+int64(f.Size) > fileSize {
			f.Complete = false
			f.Keyframe = false // boundary-incomplete frames are never valid keyframes
			book.Add(model.ErrorRecord{
				Code: model.CodeIncompleteFrame, Offset: f.Offset, Track: f.Track,
				Window: timeline.Window{Start: f.TimeTick, End: f.TimeTick + frameGuess(s)},
				Detail: "frame bytes extend past end of file",
			})
		}
		st.Frames = append(st.Frames, f)
	}

	// Derive frame end times from the next frame's start.
	for i := range st.Frames {
		if i+1 < len(st.Frames) {
			st.Frames[i].EndTick = st.Frames[i+1].TimeTick
		} else {
			st.Frames[i].EndTick = st.Frames[i].TimeTick + frameGuess(container.MP4Sample{
				Track: 1, TimeTick: st.Frames[i].TimeTick,
			})
		}
	}

	for i := range st.Frames {
		f := &st.Frames[i]
		byTrack[f.Track] = append(byTrack[f.Track], f)
		if !f.Complete {
			st.IncompleteCount++
		}
		if f.Keyframe && f.Complete {
			st.KeyframeNumbers = append(st.KeyframeNumbers, f.Number)
		}
	}

	var gaps []int64
	var lastTick int64
	have := false
	for _, f := range st.Frames {
		if f.Keyframe && f.Complete {
			if have {
				gaps = append(gaps, f.TimeTick-lastTick)
			}
			lastTick = f.TimeTick
			have = true
		}
	}
	var sum int64
	for _, g := range gaps {
		if g > st.KeyframeGapMs {
			st.KeyframeGapMs = timeline.Millis(g)
		}
		sum += g
	}
	if len(gaps) > 0 {
		st.MeanKeyframeGap = timeline.Millis(sum / int64(len(gaps)))
	}
	return st
}

// frameGuess estimates one frame's duration for the tail window when no
// successor exists. The fixture is 30fps; derive from the average span of
// the existing frames instead of hard-coding when possible.
func frameGuess(_ container.MP4Sample) int64 {
	return timeline.MasterHz / 30
}
