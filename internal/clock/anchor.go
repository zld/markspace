package clock

import (
	"sort"

	"example.com/mediatimeline/internal/model"
)

// Anchor is one track's first extended timestamp, used by the fan-in stage to
// pick a common origin. Timescale is the track clock.
type Anchor struct {
	Track      model.TrackID
	FirstTicks int64
	Timescale  uint32
}

// CommonOrigin returns the earliest track start on the program clock. Tracks
// that start later are shifted forward relative to it by SetAnchor.
func CommonOrigin(anchors []Anchor) (Rational, error) {
	if len(anchors) == 0 {
		return Zero, nil
	}
	sorted := make([]Anchor, len(anchors))
	copy(sorted, anchors)
	sort.SliceStable(sorted, func(i, j int) bool {
		if sorted[i].Track != sorted[j].Track {
			return sorted[i].Track < sorted[j].Track
		}
		return sorted[i].FirstTicks < sorted[j].FirstTicks
	})
	best, err := FromTicks(sorted[0].FirstTicks, sorted[0].Timescale)
	if err != nil {
		return Zero, err
	}
	for _, a := range sorted[1:] {
		cur, err := FromTicks(a.FirstTicks, a.Timescale)
		if err != nil {
			return Zero, err
		}
		if cur.Less(best) {
			best = cur
		}
	}
	return best, nil
}

// TicksAt converts an exact program time into ticks at timescale.
func TicksAt(t Rational, timescale uint32) (int64, error) {
	return t.FloorTicks(timescale)
}
