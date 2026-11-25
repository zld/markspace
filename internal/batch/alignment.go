package batch

import (
	"mediascan/internal/clock"
	"mediascan/internal/errs"
)

// AlignmentToleranceMS is the inclusive cross-representation boundary
// window required by the project rules.
const AlignmentToleranceMS int64 = 20

// checkAlignment converts each representation's cumulative segment
// boundary to whole milliseconds using integer remainder accumulation,
// then compares representations at the SAME segment index against the
// 20 ms window. A divergence stops the cross-representation merge (the
// object is still hashed on its own) and counts one misalignment.
func (r *Runner) checkAlignment(obj *Object) error {
	duration := obj.TimeEnd - obj.TimeStart
	if duration < 0 {
		return errs.New(errs.KindTimeline, obj.Source, errs.Unknown,
			obj.SegmentIndex, obj.TrackID, "negative segment duration")
	}
	if r.repAccs == nil {
		r.repAccs = map[string]*clock.Accumulator{}
		r.repPrevMS = map[string]int64{}
	}
	racc, ok := r.repAccs[obj.RepID]
	if !ok {
		var err error
		racc, err = clock.NewAccumulator(obj.Timescale, 1000)
		if err != nil {
			return errs.New(errs.KindTimeline, obj.Source, errs.Unknown,
				obj.SegmentIndex, obj.TrackID, "bad timescale: %v", err)
		}
		r.repAccs[obj.RepID] = racc
	}
	added, err := racc.Add(duration)
	if err != nil {
		return errs.New(errs.KindTimeline, obj.Source, errs.Unknown,
			obj.SegmentIndex, obj.TrackID, "duration conversion failed: %v", err)
	}
	// cur is this representation's cumulative timeline position in ms.
	cur := r.repPrevMS[obj.RepID] + added
	r.repPrevMS[obj.RepID] = cur

	row := r.boundariesMS[obj.SegmentIndex]
	if row == nil {
		row = map[string]int64{}
		r.boundariesMS[obj.SegmentIndex] = row
	}
	var mismatch error
	for repID, other := range row {
		if repID == obj.RepID {
			continue
		}
		diff := cur - other
		if diff < 0 {
			diff = -diff
		}
		if diff > AlignmentToleranceMS {
			mismatch = errs.New(errs.KindTimeline, obj.Source, errs.Unknown,
				obj.SegmentIndex, obj.TrackID,
				"representation %q boundary %dms diverges from %q at %dms by %dms (> %dms)",
				obj.RepID, cur, repID, other, diff, AlignmentToleranceMS)
		}
	}
	row[obj.RepID] = cur
	return mismatch
}
