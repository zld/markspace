package index

import (
	"archivist/internal/fault"
	"archivist/internal/scan"
)

// verifyRoundTrip re-scans a rebuilt file and confirms every track decodes to
// the planned rebased timestamps, sizes and offsets. A writer defect thus
// fails the run instead of producing a silently wrong index.
func verifyRoundTrip(path string, tracks []*plannedTrack, movieTS uint32) error {
	r, err := scan.Open(path)
	if err != nil {
		return err
	}
	defer r.Close()
	c, err := r.Scan()
	if err != nil {
		return err
	}
	if c.MovieTimescale != movieTS {
		return fault.New(fault.KindInvariant, fault.StageVerify,
			"rebuilt movie timescale %d != planned %d", c.MovieTimescale, movieTS).In(path)
	}
	if len(c.Tracks) != len(tracks) {
		return fault.New(fault.KindInvariant, fault.StageVerify,
			"rebuilt %d tracks, planned %d", len(c.Tracks), len(tracks)).In(path)
	}
	byID := make(map[uint32]*scan.Track, len(c.Tracks))
	for _, t := range c.Tracks {
		byID[t.ID] = t
	}
	for _, plan := range tracks {
		got, ok := byID[plan.trackID]
		if !ok {
			return fault.New(fault.KindInvariant, fault.StageVerify,
				"rebuilt file is missing track %d", plan.trackID).In(path)
		}
		if got.Timescale != plan.timescale {
			return fault.New(fault.KindInvariant, fault.StageVerify,
				"track %d timescale %d != %d", plan.trackID, got.Timescale, plan.timescale).In(path)
		}
		if len(got.Samples) != len(plan.dts) {
			return fault.New(fault.KindInvariant, fault.StageVerify,
				"track %d has %d samples, planned %d", plan.trackID, len(got.Samples), len(plan.dts)).In(path)
		}
		for i, smp := range got.Samples {
			if smp.DTS != int64(plan.dts[i]) {
				return fault.New(fault.KindInvariant, fault.StageVerify,
					"track %d sample %d DTS %d != %d", plan.trackID, i+1, smp.DTS, plan.dts[i]).In(path)
			}
			if int64(smp.PTS) != int64(plan.dts[i]) {
				return fault.New(fault.KindInvariant, fault.StageVerify,
					"track %d sample %d PTS %d != %d", plan.trackID, i+1, smp.PTS, plan.dts[i]).In(path)
			}
			if smp.Size != plan.sizes[i] {
				return fault.New(fault.KindInvariant, fault.StageVerify,
					"track %d sample %d size %d != %d", plan.trackID, i+1, smp.Size, plan.sizes[i]).In(path)
			}
			if smp.Offset != plan.sampleOffsets[i] {
				return fault.New(fault.KindInvariant, fault.StageVerify,
					"track %d sample %d offset %d != %d", plan.trackID, i+1, smp.Offset, plan.sampleOffsets[i]).In(path)
			}
			wantKey := false
			for _, k := range plan.keyframes {
				if int(k) == i+1 {
					wantKey = true
					break
				}
			}
			if smp.Keyframe != wantKey {
				return fault.New(fault.KindInvariant, fault.StageVerify,
					"track %d sample %d keyframe %v != %v", plan.trackID, i+1, smp.Keyframe, wantKey).In(path)
			}
		}
	}
	return nil
}
