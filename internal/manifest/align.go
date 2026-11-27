// Package manifest aligns a programme schedule against the repaired media
// timeline. Every comparison uses exact clock.Rational values on the shared
// time base; floats only appear in the human-readable error report.
package manifest

import (
	"fmt"

	"github.com/zld/continuity/internal/clock"
	"github.com/zld/continuity/internal/repair"
)

// Item is one scheduled programme point.
type Item struct {
	ID    string
	Start clock.Rational
	End   clock.Rational
}

// Counts separates the three alignment failure classes required by the spec.
type Counts struct {
	Backward    int // a manifest item starts before the preceding item
	Missing     int // a scheduled point has no covering media sample
	Coverage    int // an item extends outside the repaired track's time range
}

// Result is the alignment outcome.
type Result struct {
	Counts       Counts
	// MaxError is the largest absolute timing difference between a manifest
	// start and the media sample that should carry it.
	MaxError clock.Rational
	// MaxErrorMS mirrors MaxError in milliseconds for the summary output.
	MaxErrorMS float64
	Aligned    int
}

// Align compares items against one repaired track. The track supplies the
// authoritative programme timeline; each manifest start is matched to the
// nearest sample at or before it and the difference is measured exactly.
func Align(items []Item, track *repair.RepairedTrack) (Result, error) {
	res := Result{MaxError: clock.Zero()}
	if track == nil || len(track.Samples) == 0 {
		return res, clock.NewError(clock.StageManifest, "align",
			fmt.Errorf("%w: no repaired track to align against", clock.ErrCorrupt))
	}
	tMax := track.Samples[len(track.Samples)-1].Value

	var prev clock.Rational
	havePrev := false
	for i, item := range items {
		// Class 1: non-monotonic manifest ordering.
		if havePrev && item.Start.Cmp(prev) < 0 {
			res.Counts.Backward++
		}
		prev = item.Start
		havePrev = true

		// Class 3: item extends beyond the end of repaired media.
		if item.End.Cmp(tMax) > 0 {
			res.Counts.Coverage++
			continue
		}

		// Class 2: a start earlier than the first sample has no media sample
		// that can carry it, so it is a missing item rather than a coverage one.
		idx := sampleAtOrBefore(track, item.Start)
		if idx < 0 {
			res.Counts.Missing++
			continue
		}
		diff, err := absDiff(item.Start, track.Samples[idx].Value)
		if err != nil {
			return res, clock.NewError(clock.StageManifest, "diff", err).
				WithTrack(track.ID).WithSample(uint32(i))
		}
		if diff.Cmp(res.MaxError) > 0 {
			res.MaxError = diff
		}
		res.Aligned++
	}
	res.MaxErrorMS = res.MaxError.Milliseconds()
	return res, nil
}

// sampleAtOrBefore returns the largest sample index whose time is <= target,
// or -1 when none exists.
func sampleAtOrBefore(track *repair.RepairedTrack, target clock.Rational) int {
	lo, hi := 0, len(track.Samples)
	for lo < hi {
		mid := (lo + hi) / 2
		if track.Samples[mid].Value.Cmp(target) <= 0 {
			lo = mid + 1
		} else {
			hi = mid
		}
	}
	return lo - 1
}

func absDiff(a, b clock.Rational) (clock.Rational, error) {
	switch a.Cmp(b) {
	case 0:
		return clock.Zero(), nil
	case 1:
		return a.Sub(b)
	default:
		return b.Sub(a)
	}
}

// FromTrack derives a reference manifest by sampling every Nth repaired sample,
// giving the demo and tests a schedule that should align almost perfectly.
func FromTrack(track *repair.RepairedTrack, every int) []Item {
	if every <= 0 {
		every = 1
	}
	items := make([]Item, 0, len(track.Samples)/every+1)
	for i := 0; i < len(track.Samples); i += every {
		start := track.Samples[i].Value
		end := start
		if i+1 < len(track.Samples) {
			end = track.Samples[i+1].Value
		}
		items = append(items, Item{
			ID:    fmt.Sprintf("item-%03d", i),
			Start: start,
			End:   end,
		})
	}
	return items
}
