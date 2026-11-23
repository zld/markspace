// Package clock provides the shared nanosecond time base used by every stage
// of the pipeline. This file holds read-only accessors over a calibrated
// timeline.
package clock

import (
	"sort"

	"mediad/internal/media"
)

// DriftPPM reports a track's estimated rate deviation from nominal in parts
// per million; positive means the device clock runs fast.
func (tl *Timeline) DriftPPM(t media.TrackID) (float64, bool) {
	tr, ok := tl.tracks[t]
	if !ok || tr.slope == 0 {
		return 0, false
	}
	return (tr.slope - 1) * 1e6, true
}

// Tracks returns the observed track ids in ascending order.
func (tl *Timeline) Tracks() []media.TrackID {
	ids := make([]media.TrackID, 0, len(tl.tracks))
	for id := range tl.tracks {
		ids = append(ids, id)
	}
	sort.Slice(ids, func(i, j int) bool { return ids[i] < ids[j] })
	return ids
}

// FirstShared returns the shared-clock time that a track's first observation
// maps to, once calibrated.
func (tl *Timeline) FirstShared(t media.TrackID) (int64, bool) {
	tr, ok := tl.tracks[t]
	if !ok || !tr.haveShare {
		return 0, false
	}
	return tr.baseShare, true
}
