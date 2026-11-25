package mp4scan

import (
	"archivecheck/internal/clock"
	apperrors "archivecheck/internal/errors"
	"archivecheck/internal/model"
)

func validateRequestedIntervals(requested []model.EditInterval, r *ScanResult) *apperrors.Error {
	for _, in := range requested {
		idx := in.Index
		if in.Empty {
			return apperrors.E(apperrors.KindEditList, "empty edit interval rejected").WithInterval(idx)
		}
		if in.EndTick < in.StartTick {
			return apperrors.E(apperrors.KindEditList, "reverse edit interval rejected").WithInterval(idx)
		}
		if err := intervalBounds(in, r); err != nil {
			return err.WithInterval(idx)
		}
	}
	return nil
}

func intervalBounds(in model.EditInterval, r *ScanResult) *apperrors.Error {
	if len(r.Samples) == 0 {
		return apperrors.E(apperrors.KindNoKeyframe, "no samples for interval bounds")
	}
	first := r.Samples[0].PTS
	last := r.Samples[len(r.Samples)-1].PTS + int64(r.SampleDurationTick)
	if in.StartTick < first-1 || in.StartTick > last+1 || in.EndTick < first-1 || in.EndTick > last+1 {
		return apperrors.E(apperrors.KindEditList, "edit interval is outside sampled media timeline")
	}
	if in.EndTick <= in.StartTick {
		return apperrors.E(apperrors.KindEditList, "edit interval has non-positive duration")
	}
	return nil
}

func buildTimeline(r *ScanResult) {
	containerStart := r.Samples[0].PTS
	zeroPoint := containerStart
	if len(r.Edits) > 0 && !r.Edits[0].Empty && r.Edits[0].MediaTime != 0 {
		zeroPoint = r.Edits[0].MediaTime
	}
	editedDuration := clock.Tick(0)
	for _, e := range r.Edits {
		editedDuration += clock.Tick(e.SegmentDuration)
	}
	if editedDuration == 0 {
		last := r.Samples[len(r.Samples)-1]
		editedDuration = clock.Tick(last.PTS - containerStart + int64(r.SampleDurationTick))
	}
	r.Timeline = clock.Timeline{
		Timescale:             r.Timebase.Timescale,
		ContainerStart:        clock.Tick(containerStart),
		EditZeroPoint:         clock.Tick(zeroPoint),
		EditedStart:           0,
		EditedDuration:        editedDuration,
		NonZeroStartPreserved: true,
	}
}
