package manifest

import (
	"mediacore/internal/clock"
	"mediacore/internal/fault"
)

// AlignmentError compares two manifests entry by entry and returns the
// largest absolute timestamp difference in nanoseconds. Entries are matched
// by ID; a missing ID or a different entry count is an error.
func AlignmentError(a, b *Manifest) (clock.Time, error) {
	index := make(map[string]Item, len(b.Items))
	for _, it := range b.Items {
		index[it.ID] = it
	}
	var worst clock.Time
	for _, it := range a.Items {
		other, ok := index[it.ID]
		if !ok {
			return 0, fault.Newf(fault.CodeManifestCount, "",
				"entry %q is missing from the second manifest", it.ID)
		}
		d := other.PTS - it.PTS
		if d < 0 {
			d = -d
		}
		if d > worst {
			worst = d
		}
	}
	if len(a.Items) != len(b.Items) {
		return worst, fault.Newf(fault.CodeManifestCount, "",
			"entry counts differ: %d vs %d", len(a.Items), len(b.Items))
	}
	return worst, nil
}
