package mp4scan

import (
	"github.com/abema/go-mp4"

	"archivecheck/internal/clock"
	apperrors "archivecheck/internal/errors"
	"archivecheck/internal/model"
)

func buildEdits(r *ScanResult, mediaTB, movieTB clock.Timebase, mdhd *mp4.Mdhd, elst *mp4.Elst, requested []model.EditInterval) *apperrors.Error {
	if elst == nil {
		for _, in := range requested {
			r.Edits = append(r.Edits, EditInfo{
				Index: in.Index, ManifestStartTick: in.StartTick, ManifestEndTick: in.EndTick,
				StartTick: in.StartTick, EndTick: in.EndTick, Empty: in.Empty,
			})
		}
		return validateRequestedIntervals(requested, r)
	}

	for i, e := range elst.Entries {
		var segmentDuration, mediaTime int64
		if elst.GetVersion() == 1 {
			segmentDuration = int64(e.SegmentDurationV1)
			mediaTime = e.MediaTimeV1
		} else {
			segmentDuration = int64(e.SegmentDurationV0)
			mediaTime = int64(e.MediaTimeV0)
		}
		empty := mediaTime == -1
		info := EditInfo{
			Index:           i,
			SegmentDuration: int64(movieTB.ToShared(segmentDuration)),
			MediaTime:       int64(mediaTB.ToShared(mediaTime)),
			Empty:           empty,
		}
		if mediaTime >= 0 {
			info.StartTick = info.MediaTime
			info.EndTick = info.StartTick + info.SegmentDuration
		}
		if i < len(requested) {
			info.ManifestStartTick = requested[i].StartTick
			info.ManifestEndTick = requested[i].EndTick
		}
		r.Edits = append(r.Edits, info)

		idx := i
		if i < len(requested) {
			idx = requested[i].Index
		}
		if empty || (i < len(requested) && requested[i].Empty) {
			return apperrors.E(apperrors.KindEditList, "empty edit interval rejected").WithInterval(idx)
		}
		if e.MediaRateInteger < 0 || (i < len(requested) && requested[i].EndTick < requested[i].StartTick) {
			return apperrors.E(apperrors.KindEditList, "reverse edit interval rejected").WithInterval(idx)
		}
		if mdhd != nil && mediaTime >= int64(mdhd.GetDuration()) {
			return apperrors.E(apperrors.KindEditList, "edit media_time is outside media duration").WithInterval(idx)
		}
		if i < len(requested) {
			if err := intervalBounds(requested[i], r); err != nil {
				return err.WithInterval(requested[i].Index)
			}
			if clock.Abs(clock.Tick(info.StartTick), clock.Tick(requested[i].StartTick)) > 1 {
				return apperrors.E(apperrors.KindEditList, "manifest edit start does not match elst").WithInterval(idx)
			}
		}
	}
	return validateRequestedIntervals(requested, r)
}
