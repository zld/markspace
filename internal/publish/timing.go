package publish

import (
	"regress/internal/fault"
	"regress/internal/timebase"
)

// RepresentationTiming is one rendition's total duration in its own track
// timescale plus the source clock denominator it is compared against.
type RepresentationTiming struct {
	Name      string
	TotalTicks uint64
	Timescale uint64
}

// CheckRepresentationDurations enforces requirement that renditions differ by
// at most one source clock tick. All values are converted through the shared
// integer nanosecond time base; any larger gap is a boundary discontinuity.
func CheckRepresentationDurations(sourceDen uint64, reps []RepresentationTiming) error {
	if len(reps) < 2 {
		return nil
	}
	ref, err := timebase.New(reps[0].TotalTicks, reps[0].Timescale)
	if err != nil {
		return err
	}
	for i := 1; i < len(reps); i++ {
		cur, err := timebase.New(reps[i].TotalTicks, reps[i].Timescale)
		if err != nil {
			return err
		}
		ok, err := timebase.WithinOneSourceTick(ref, cur, sourceDen)
		if err != nil {
			return err
		}
		if !ok {
			return fault.New(fault.StagePublish, fault.CodeBoundaryDiscontinuity, "",
				"representation duration exceeds one source tick: "+reps[i].Name)
		}
	}
	return nil
}

// TotalDurationTicks sums a segment plan's per-segment durations as read from
// the manifest, giving a representation's total track-timescale duration.
func TotalDurationTicks(entries []ManifestEntry) uint64 {
	var total uint64
	for _, e := range entries {
		total += e.DurationTicks
	}
	return total
}
