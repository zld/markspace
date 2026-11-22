package syncer

import (
	"sort"

	"mediacore"
)

// AlignStatus reports how an audio block sits relative to its nearest frame.
type AlignStatus uint8

const (
	AlignOK      AlignStatus = iota // inside tolerance
	AlignSkew                       // nearest frame is farther than tolerance
	AlignBeforeFirst                // audio precedes the first video frame
	AlignAfterLast                  // audio outlasts the last video frame
)

// AlignResult binds one audio block to its nearest video frame and records
// the signed integer-nanosecond offset between them.
type AlignResult struct {
	Audio  mediacore.Event
	Frame  FrameSample
	Offset int64
	Status AlignStatus
}

// Aligner pairs an audio track with a VFR video track on the shared time
// base. Alignment is deterministic: nearest-frame selection is symmetric and
// ties resolve to the earlier frame.
type Aligner struct {
	audio  mediacore.TrackID
	video  mediacore.TrackID
	tolNS  int64
	frames []FrameSample
}

// NewAligner builds an aligner from a validated VFR mapper. toleranceNS is the
// maximum acceptable audio-to-frame offset.
func NewAligner(vfr *VFRMapper, audio, video mediacore.TrackID, toleranceNS int64) *Aligner {
	frames := append([]FrameSample(nil), vfr.Frames()...)
	sort.Slice(frames, func(i, j int) bool { return frames[i].Time < frames[j].Time })
	return &Aligner{audio: audio, video: video, tolNS: toleranceNS, frames: frames}
}

// Align binds every audio block to its nearest frame. Empty either side
// produces a classified empty error; out-of-range blocks get explicit
// boundary statuses rather than being silently clamped.
func (a *Aligner) Align(blocks []mediacore.Event) ([]AlignResult, error) {
	if len(a.frames) == 0 {
		return nil, mediacore.NewError(mediacore.CatEmpty, a.video, "align.video", 0,
			"cannot align against an empty video track")
	}
	if len(blocks) == 0 {
		return nil, mediacore.NewError(mediacore.CatEmpty, a.audio, "align.audio", 0,
			"cannot align an empty audio track")
	}
	out := make([]AlignResult, 0, len(blocks))
	for _, blk := range blocks {
		r := AlignResult{Audio: blk}
		if blk.Time < a.frames[0].Time {
			r.Status = AlignBeforeFirst
			r.Frame = a.frames[0]
			r.Offset = blk.Time - a.frames[0].Time
			out = append(out, r)
			continue
		}
		last := a.frames[len(a.frames)-1]
		if blk.Time > last.Time {
			r.Status = AlignAfterLast
			r.Frame = last
			r.Offset = blk.Time - last.Time
			out = append(out, r)
			continue
		}
		r.Frame, r.Offset = a.nearest(blk.Time)
		if abs64(r.Offset) > a.tolNS {
			r.Status = AlignSkew
		}
		out = append(out, r)
	}
	return out, nil
}

// nearest returns the closest frame and the signed offset (audio minus frame).
// Exact midpoint ties resolve to the earlier frame.
func (a *Aligner) nearest(t int64) (FrameSample, int64) {
	i := sort.Search(len(a.frames), func(i int) bool { return a.frames[i].Time >= t })
	if i == 0 {
		return a.frames[0], t - a.frames[0].Time
	}
	if i >= len(a.frames) {
		last := a.frames[len(a.frames)-1]
		return last, t - last.Time
	}
	prev, next := a.frames[i-1], a.frames[i]
	if next.Time == t {
		return next, 0
	}
	dPrev, dNext := t-prev.Time, next.Time-t
	if dNext < dPrev {
		return next, -dNext
	}
	return prev, dPrev // tie -> earlier frame
}

// MaxSkew returns the largest absolute offset across the results and how many
// blocks exceeded tolerance.
func MaxSkew(rs []AlignResult) (maxNS int64, skewed int) {
	for _, r := range rs {
		if d := abs64(r.Offset); d > maxNS {
			maxNS = d
		}
		if r.Status == AlignSkew {
			skewed++
		}
	}
	return maxNS, skewed
}
