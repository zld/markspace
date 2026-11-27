// Package syncer aligns audio and video events on the shared
// nanosecond timeline and reports the maximum A/V deviation.
package syncer

import (
	"sort"

	"continuity/internal/merge"
)

// Summary describes the alignment result.
type Summary struct {
	Pairs          int
	MaxDeviationNs int64
}

// Align matches every video event with the nearest audio event and
// returns the deviation statistics. events must be sorted by
// (timestamp, track, index) as produced by merge.Merge.
func Align(events []merge.Event) Summary {
	var audios []int64
	for _, e := range events {
		if e.Kind == "audio" {
			audios = append(audios, e.TimestampNs)
		}
	}
	var s Summary
	if len(audios) == 0 {
		return s
	}
	for _, e := range events {
		if e.Kind != "video" {
			continue
		}
		i := sort.Search(len(audios), func(i int) bool { return audios[i] >= e.TimestampNs })
		best := int64(-1)
		if i < len(audios) {
			best = abs(audios[i] - e.TimestampNs)
		}
		if i > 0 {
			if d := abs(audios[i-1] - e.TimestampNs); best < 0 || d < best {
				best = d
			}
		}
		s.Pairs++
		if best > s.MaxDeviationNs {
			s.MaxDeviationNs = best
		}
	}
	return s
}

func abs(v int64) int64 {
	if v < 0 {
		return -v
	}
	return v
}
