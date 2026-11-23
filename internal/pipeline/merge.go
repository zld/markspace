package pipeline

import (
	"fmt"
	"sort"

	"mediaarchive/internal/timeline"
	"mediaarchive/internal/track"
)

// Event is the normalized unit that flows from the worker pool into the
// merger. It is produced from EBML block events and recovered RTP slots.
type Event struct {
	Track     uint32
	TimeNanos int64
	SourceSeq int64 // input order; final tie-breaker for stable merging
	Kind      track.Kind
	Payload   []byte
	Recovered bool // RTP packet-loss concealment placeholder
	Gap       bool // unrecovered loss marker
}

// ValidateSourceOrder checks that each track's timestamps are non-decreasing
// in arrival/source order. This is a data-quality guard used on events as
// they are delivered by the EBML parser or RTP recovery layer, before
// interleaving is resolved. A violation means clock drift or bad recovery;
// it is deliberately separate from Merge, whose output is always sorted and
// therefore independent of input permutation.
func ValidateSourceOrder(events []Event) error {
	seen := make(map[uint32]int64)
	for _, ev := range events {
		if prev, ok := seen[ev.Track]; ok {
			if err := timeline.CheckMonotonic(prev, ev.TimeNanos, ev.Track); err != nil {
				return err
			}
		}
		seen[ev.Track] = ev.TimeNanos
	}
	return nil
}

// Merge orders events deterministically by (time, track, input sequence).
// The sort is stable, so any permutation of the same events yields the exact
// same output — this is what makes repeated archive runs byte-identical.
// After sorting, per-track timestamps are necessarily non-decreasing; the
// result is re-validated as a defense-in-depth invariant. The input slice
// is not mutated; a new sorted slice is returned.
func Merge(events []Event) ([]Event, error) {
	out := make([]Event, len(events))
	copy(out, events)
	sort.SliceStable(out, func(i, j int) bool {
		if out[i].TimeNanos != out[j].TimeNanos {
			return out[i].TimeNanos < out[j].TimeNanos
		}
		if out[i].Track != out[j].Track {
			return out[i].Track < out[j].Track
		}
		return out[i].SourceSeq < out[j].SourceSeq
	})
	if err := ValidateSourceOrder(out); err != nil {
		return nil, err
	}
	return out, nil
}

// MustMerge is Merge for callers that already validated their inputs; it
// panics on a monotonicity violation rather than returning an error.
func MustMerge(events []Event) []Event {
	out, err := Merge(events)
	if err != nil {
		panic(fmt.Sprintf("pipeline: merge invariant broken: %v", err))
	}
	return out
}

// TimeRange returns the inclusive nanosecond span of a merged event slice.
// An empty slice reports zeroes.
func TimeRange(events []Event) (first, last int64) {
	if len(events) == 0 {
		return 0, 0
	}
	return events[0].TimeNanos, events[len(events)-1].TimeNanos
}

// CountByTrack groups event counts, keyed by track number for manifests.
func CountByTrack(events []Event) map[uint32]int {
	m := make(map[uint32]int)
	for _, ev := range events {
		m[ev.Track]++
	}
	return m
}
