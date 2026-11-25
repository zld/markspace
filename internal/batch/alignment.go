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
// then compares representations of the SAME manifest kind at the SAME
// segment index against the 20 ms window. DASH and HLS are independent
// timelines and never compared. A divergence stops the cross-rep merge
// (the object is still hashed alone) and counts one misalignment.
func (r *Runner) checkAlignment(obj *Object) error {
	if obj.ManifestKind == "corrupt" {
		return nil // damaged inputs are not part of alignment
	}
	duration := obj.TimeEnd - obj.TimeStart
	if duration < 0 {
		return errs.New(errs.KindTimeline, obj.Source, errs.Unknown,
			obj.SegmentIndex, obj.TrackID, "negative segment duration")
	}
	accKey := obj.ManifestKind + "/" + obj.RepID
	if r.repAccs == nil {
		r.repAccs = map[string]*clock.Accumulator{}
		r.repPrevMS = map[string]int64{}
	}
	racc, ok := r.repAccs[accKey]
	if !ok {
		var err error
		racc, err = clock.NewAccumulator(obj.Timescale, 1000)
		if err != nil {
			return errs.New(errs.KindTimeline, obj.Source, errs.Unknown,
				obj.SegmentIndex, obj.TrackID, "bad timescale: %v", err)
		}
		r.repAccs[accKey] = racc
	}
	added, err := racc.Add(duration)
	if err != nil {
		return errs.New(errs.KindTimeline, obj.Source, errs.Unknown,
			obj.SegmentIndex, obj.TrackID, "duration conversion failed: %v", err)
	}
	// cur is this representation's cumulative timeline position in ms.
	cur := r.repPrevMS[accKey] + added
	r.repPrevMS[accKey] = cur

	kindRow := r.boundariesMS[obj.ManifestKind]
	if kindRow == nil {
		kindRow = map[int64]map[string]int64{}
		r.boundariesMS[obj.ManifestKind] = kindRow
	}
	row := kindRow[obj.SegmentIndex]
	if row == nil {
		row = map[string]int64{}
		kindRow[obj.SegmentIndex] = row
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
				"%s representation %q boundary %dms diverges from %q at %dms by %dms (> %dms)",
				obj.ManifestKind, obj.RepID, cur, repID, other, diff, AlignmentToleranceMS)
		}
	}
	row[obj.RepID] = cur
	return mismatch
}
