package core

import (
	"fmt"

	"mediad/internal/model"
)

// SplitOptions controls keyframe-aligned chunking.
type SplitOptions struct {
	TargetDuration model.PlayTime `json:"target_duration_ns"`
}

// Boundary records where a chunk actually starts and why it differs from
// the ideal target-duration boundary.
type Boundary struct {
	IdealAt      model.PlayTime `json:"ideal_at_ns"`
	ActualAt     model.PlayTime `json:"actual_at_ns"`
	KeyFramePTS  model.PlayTime `json:"keyframe_pts_ns"`
	Hit          bool           `json:"hit"` // exact keyframe at the ideal boundary
	SourceEntry  int            `json:"source_entry"`
	SourceFrame  int            `json:"source_frame"`
}

// Chunk is one output slice of the timeline.
type Chunk struct {
	ID           string         `json:"id"`
	Frames       []model.Frame  `json:"frames"`
	Start        model.PlayTime `json:"start_ns"`
	End          model.PlayTime `json:"end_ns"`
	Boundary     Boundary       `json:"boundary"`
	Representation string       `json:"representation"`
}

// SplitResult is the splitter output plus the keyframe-hit statistic.
type SplitResult struct {
	Chunks        []Chunk `json:"chunks"`
	KeyframeHits  int     `json:"keyframe_hits"`
	BoundaryTotal int     `json:"boundary_total"`
}

// HitRate returns the fraction of internal boundaries that landed exactly
// on a keyframe. A timeline with a single chunk reports 1.0 by construction.
func (r SplitResult) HitRate() float64 {
	if r.BoundaryTotal == 0 {
		return 1.0
	}
	return float64(r.KeyframeHits) / float64(r.BoundaryTotal)
}

// Split walks the timeline and emits chunks of roughly TargetDuration,
// snapping every boundary back to the closest preceding keyframe. The first
// frame is always treated as a keyframe anchor; non-keyframe-only streams
// still split but every internal boundary records Hit=false.
func Split(repID string, frames []model.Frame, opts SplitOptions) (*SplitResult, error) {
	if opts.TargetDuration <= 0 {
		return nil, model.NewError(model.KindFieldOutOfRange,
			model.SourceLoc{Representation: repID, Frame: -1, Entry: -1},
			fmt.Sprintf("target duration %d must be positive", opts.TargetDuration))
	}
	if len(frames) == 0 {
		return nil, model.NewError(model.KindTruncatedFrame,
			model.SourceLoc{Representation: repID, Frame: -1, Entry: -1},
			"cannot split an empty frame stream")
	}
	res := &SplitResult{}
	startIdx := 0
	chunkNo := 0
	for startIdx < len(frames) {
		ideal := frames[startIdx].PTS + opts.TargetDuration
		endIdx, boundary := nextBoundary(frames, startIdx, ideal)
		if endIdx <= startIdx {
			// The snapped keyframe collapsed onto the chunk start (the key
			// frame is the first frame of this chunk). Advance at least one
			// frame and record the boundary miss honestly.
			endIdx = startIdx + 1
			if endIdx < len(frames) {
				boundary = Boundary{
					IdealAt: ideal, ActualAt: frames[endIdx].PTS,
					KeyFramePTS: -1, Hit: false, SourceFrame: endIdx,
				}
			}
		}
		switch {
		case chunkNo == 0:
			// The timeline origin is always an exact keyframe anchor.
			boundary = Boundary{
				IdealAt: frames[0].PTS, ActualAt: frames[0].PTS,
				KeyFramePTS: frames[0].PTS, Hit: true, SourceFrame: 0,
			}
		case endIdx >= len(frames):
			// Final tail chunk: there is no following boundary, so it does
			// not participate in the keyframe-hit statistic.
			boundary = Boundary{IdealAt: ideal, ActualAt: 0, Hit: false, SourceFrame: -1}
		default:
			res.BoundaryTotal++
			if boundary.Hit {
				res.KeyframeHits++
			}
		}
		res.Chunks = append(res.Chunks, makeChunk(repID, chunkNo, frames, startIdx, endIdx, boundary))
		startIdx = endIdx
		chunkNo++
	}
	return res, nil
}

// nextBoundary returns the exclusive end index for the chunk beginning at
// startIdx and the boundary descriptor for the following chunk.
//
// The boundary is the last keyframe whose PTS is at or before the ideal
// boundary and strictly inside the current chunk. When no such keyframe
// exists, the ideal edge (first frame past the target) is kept and Hit=false
// is recorded honestly rather than jumping to a keyframe in a later span.
// The result is len(frames) when the remaining frames fit one target span.
func nextBoundary(frames []model.Frame, startIdx int, ideal model.PlayTime) (int, Boundary) {
	edge := startIdx + 1
	for edge < len(frames) && frames[edge].PTS < ideal {
		edge++
	}
	if edge >= len(frames) {
		return len(frames), Boundary{IdealAt: ideal, ActualAt: 0, Hit: false, SourceFrame: -1}
	}
	// Search only within [startIdx+1, edge]: the boundary must not reach
	// past the ideal time or collapse onto the current chunk start.
	kf := -1
	for i := edge; i > startIdx; i-- {
		if frames[i].KeyFrame && frames[i].PTS <= ideal {
			kf = i
			break
		}
	}
	if kf < 0 {
		return edge, Boundary{
			IdealAt: ideal, ActualAt: frames[edge].PTS,
			KeyFramePTS: -1, Hit: false, SourceFrame: edge,
		}
	}
	hit := frames[kf].PTS == ideal
	return kf, Boundary{
		IdealAt: ideal, ActualAt: frames[kf].PTS,
		KeyFramePTS: frames[kf].PTS, Hit: hit, SourceFrame: kf,
	}
}

func makeChunk(repID string, no int, frames []model.Frame, start, end int, b Boundary) Chunk {
	cf := append([]model.Frame(nil), frames[start:end]...)
	return Chunk{
		ID: fmt.Sprintf("%s-c%04d", repID, no),
		Frames: cf,
		Start:  frames[start].PTS,
		End:    frames[end-1].PTS + frames[end-1].Dur,
		Boundary: b,
		Representation: repID,
	}
}
