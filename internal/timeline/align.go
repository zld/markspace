// Package timeline maps track-local timestamps onto the shared
// rational time base and applies the crop/rotation/colour policy.
package timeline

import (
	"math"

	"archive/internal/model"
	"archive/internal/parse"
)

// AlignedSample is one sample positioned on the unified time base.
type AlignedSample struct {
	Track       int            `json:"track"`
	SampleIndex uint32         `json:"sample_index"`
	Start       model.Rational `json:"start"` // seconds on the unified base
	Duration    model.Rational `json:"duration"`
	Keyframe    bool           `json:"keyframe"`
}

// AlignTrack converts one parsed track onto the unified time base.
// The RTP sync timestamp (90 kHz clock) anchors the track origin and
// non-zero media offsets are honoured. All arithmetic is exact
// rational; any overflow or non-integral conversion fails with the
// track number and the failing timestamp attached.
func AlignTrack(trackIdx int, tr *parse.Track) ([]AlignedSample, error) {
	base := model.Rational{Num: 0, Den: 1}
	if tr.HasRTP {
		b, err := model.FromTicks(int64(tr.RTPTimestamp), model.UnifiedScale)
		if err != nil {
			return nil, model.StageFault(model.StageAlign, trackIdx, -1, nil, "invalid RTP base: %v", err)
		}
		base = b
	}
	out := make([]AlignedSample, 0, len(tr.Samples))
	for _, s := range tr.Samples {
		if s.DTS > math.MaxInt64-tr.MediaOffset {
			return nil, model.StageFault(model.StageAlign, trackIdx, -1, nil,
				"track timestamp overflow: media_offset %d + dts %d", tr.MediaOffset, s.DTS)
		}
		local, err := model.FromTicks(tr.MediaOffset+s.DTS, tr.TimeScale)
		if err != nil {
			return nil, model.StageFault(model.StageAlign, trackIdx, -1, nil, "invalid local time: %v", err)
		}
		start, err := base.Add(local)
		if err != nil {
			t := local
			return nil, model.StageFault(model.StageAlign, trackIdx, -1, &t,
				"unified time overflow for sample %d: %v", s.Index, err)
		}
		dur, err := model.FromTicks(s.Duration, tr.TimeScale)
		if err != nil {
			return nil, model.StageFault(model.StageAlign, trackIdx, -1, &start, "invalid duration: %v", err)
		}
		// The unified position must be exactly representable; this is
		// where timestamp overflow or incommensurate scales surface.
		if _, err := start.ToTicks(model.UnifiedScale); err != nil {
			t := start
			return nil, model.StageFault(model.StageAlign, trackIdx, -1, &t,
				"sample %d not representable on unified scale: %v", s.Index, err)
		}
		if len(out) > 0 && out[len(out)-1].Start.Cmp(start) >= 0 {
			t := start
			return nil, model.StageFault(model.StageAlign, trackIdx, -1, &t,
				"non-monotonic sample time at sample %d", s.Index)
		}
		out = append(out, AlignedSample{
			Track: trackIdx, SampleIndex: s.Index,
			Start: start, Duration: dur, Keyframe: s.Keyframe,
		})
	}
	return out, nil
}
