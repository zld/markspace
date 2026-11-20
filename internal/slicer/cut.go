package slicer

import (
	"errors"
	"fmt"
	"io"
	"time"

	"hlscheck/internal/model"
	"hlscheck/internal/ts"
)

// ErrNoKeyframeInWindow means no IDR frame existed inside the permitted cut
// window; the slicer refuses to emit a segment that does not start on one.
var ErrNoKeyframeInWindow = errors.New("no IDR keyframe available within cut window")

// ErrNoVideo means the stream never exposed a video PID via PAT/PMT.
var ErrNoVideo = errors.New("no video elementary stream discovered")

// Cut describes one decided segment boundary.
type Cut struct {
	// StartPTS is the unwrapped PTS of the segment's first frame.
	StartPTS int64
	// StartOffset is the byte offset of the boundary's first TS packet.
	StartOffset int64
	// Keyframe marks that this boundary really lands on an IDR frame.
	Keyframe bool
}

// Plan holds the boundaries derived during the analysis pass.
type Plan struct {
	Cuts []Cut
	// EndPTS is the PTS one frame past the final frame.
	EndPTS int64
	// FrameTicks is the detected per-frame duration in 90 kHz ticks.
	FrameTicks int64
	// CCErrors are continuity problems observed during analysis.
	CCErrors []ts.CCError
}

// Analyze performs a single bounded-memory pass over the stream and chooses
// keyframe boundaries incrementally. Only chosen cuts are retained, so
// resident state scales with the number of segments, never total frames.
//
// Rule: the first IDR starts segment 0; afterward a cut is taken at the first
// IDR whose PTS reaches the running target. If no IDR exists within twice
// the target of the previous cut, ErrNoKeyframeInWindow is returned rather
// than starting a segment on a non-keyframe.
func Analyze(r io.Reader, target time.Duration) (*Plan, error) {
	demux := ts.NewDemuxer(r)
	targetTicks := model.Ticks(target)

	var (
		cuts        []Cut
		nextTarget  int64
		haveFirst   bool
		lastPTS     int64
		prevPTS     int64
		frameTicks  int64
		frameCount  int
		sawKeyframe bool
	)

	consider := func(pts, offset int64) error {
		if !haveFirst {
			cuts = append(cuts, Cut{StartPTS: pts, StartOffset: offset, Keyframe: true})
			nextTarget = pts + targetTicks
			haveFirst = true
			return nil
		}
		if pts < nextTarget {
			return nil // keep scanning until the nominal target is reached
		}
		lastCut := cuts[len(cuts)-1].StartPTS
		if pts > lastCut+2*targetTicks {
			return fmt.Errorf("%w: after PTS %d next IDR at %d",
				ErrNoKeyframeInWindow, lastCut, pts)
		}
		cuts = append(cuts, Cut{StartPTS: pts, StartOffset: offset, Keyframe: true})
		nextTarget = pts + targetTicks
		return nil
	}

	for demux.Scan() {
		f := demux.LastFrame
		if frameCount == 1 {
			frameTicks = f.PTS - prevPTS
		}
		prevPTS, lastPTS = f.PTS, f.PTS
		frameCount++
		if !f.Keyframe {
			continue
		}
		sawKeyframe = true
		if err := consider(f.PTS, f.StartOffset); err != nil {
			return nil, err
		}
	}
	if err := demux.LastErr; err != nil && !errors.Is(err, io.EOF) {
		return nil, err
	}
	if flushed := demux.Flush(); flushed != nil {
		lastPTS = flushed.PTS
		if flushed.Keyframe {
			sawKeyframe = true
			if err := consider(flushed.PTS, flushed.StartOffset); err != nil {
				return nil, err
			}
		}
	}
	if demux.VideoPID() < 0 {
		return nil, ErrNoVideo
	}
	if !sawKeyframe {
		return nil, ErrNoKeyframeInWindow
	}
	if frameTicks <= 0 {
		frameTicks = model.FrameTolerance // fall back to one 25 fps frame
	}
	endPTS := lastPTS + frameTicks
	// A stream ending before the next permitted IDR leaves an over-long final
	// segment. Reject it so a wide-GOP source cannot silently violate the
	// keyframe window guarantee.
	lastCut := cuts[len(cuts)-1].StartPTS
	if endPTS-lastCut > 2*targetTicks {
		return nil, fmt.Errorf("%w: final segment from PTS %d runs %d ticks, over twice the %d tick target",
			ErrNoKeyframeInWindow, lastCut, endPTS-lastCut, targetTicks)
	}
	return &Plan{
		Cuts:       cuts,
		EndPTS:     endPTS,
		FrameTicks: frameTicks,
		CCErrors:   demux.CCErrors(),
	}, nil
}
