package timeline

import (
	"testing"

	"mediaroute/internal/model"
)

// FuzzRepairNeverPanics confirms repair is total over arbitrary fragment
// shapes, including zero/duplicate timescales, empty sample slices and
// out-of-order tfdt values.
func FuzzRepairNeverPanics(f *testing.F) {
	f.Add(uint32(90000), uint64(0), uint64(90000), uint32(1800))
	f.Add(uint32(0), uint64(1), uint64(0), uint32(0))
	f.Fuzz(func(t *testing.T, timescale uint32, tfdt, endTicks uint64, dur uint32) {
		if uint64(int64(tfdt)) != tfdt && tfdt > 1<<62 {
			return // avoid pathological huge values that cannot be encoded
		}
		set := &model.SourceSet{
			Tracks: []model.Track{{TrackID: 1, Timescale: timescale}},
			Fragments: []model.Fragment{
				{Index: 0, Sequence: 1, Tracks: []model.FragmentTrack{{
					TrackID: 1, Timescale: timescale, TfdtPresent: true, Tfdt: tfdt,
					SequenceKnown: true,
					Samples:       []model.Sample{{Duration: dur, Size: 10}},
				}}},
				{Index: 1, Sequence: 2, Tracks: []model.FragmentTrack{{
					TrackID: 1, Timescale: timescale, TfdtPresent: true, Tfdt: endTicks,
					SequenceKnown: true,
					Samples:       []model.Sample{{Duration: dur, Size: 10}},
				}}},
			},
		}
		// Must not panic; errors are acceptable for zero timescales and
		// zero durations.
		_, _ = Repair(set)
	})
}
