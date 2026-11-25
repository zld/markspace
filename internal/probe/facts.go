package probe

import "mediaflow/internal/fault"

func detectTimelineFacts(t *Track) []fault.Interval {
	if t.Timescale == 0 {
		return []fault.Interval{{
			Kind: "invalid_timebase", Class: fault.ClassTimeBase,
			TrackID: t.ID, TrackNumber: t.Number, Reject: true,
			Message: "track timescale is zero",
		}}
	}
	if len(t.Samples) == 0 {
		return []fault.Interval{{
			Kind: "empty_sample_table", Class: fault.ClassMissingTrack,
			TrackID: t.ID, TrackNumber: t.Number, Reject: true,
			Message: "track contains no samples",
		}}
	}
	return nil
}
