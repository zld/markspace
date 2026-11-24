package timeline

import (
	"sort"

	"mediasync/internal/model"
)

// Replay re-derives a timeline deterministically from the earliest affected
// checkpoint. The kernel keeps a prefix of released events; when a late event
// arrives, every event at or after the watermark is replayed in canonical
// order together with the late event. Because ordering uses the events' own
// timestamps and stable tie-breakers, the digest after replay depends only on
// the multiset of events, not on arrival order.
type Replay struct {
	events []Sample
	// maxEvents bounds retained history. When exceeded the oldest events are
	// dropped; those are already before the watermark and never replayed.
	maxEvents int
	dropped   int64
}

// NewReplay builds a replay log with a bounded window.
func NewReplay(maxEvents int) (*Replay, error) {
	if maxEvents <= 0 {
		return nil, model.NewError(model.CodeBufferOverflow, model.Loc{TrackIndex: -1}, "max replay events must be positive, got %d", maxEvents)
	}
	return &Replay{maxEvents: maxEvents}, nil
}

// Append records an event.
func (r *Replay) Append(s Sample) {
	r.events = append(r.events, s)
	if len(r.events) > r.maxEvents {
		drop := len(r.events) - r.maxEvents
		r.events = append([]Sample(nil), r.events[drop:]...)
		r.dropped += int64(drop)
	}
}

// Earliest returns the indexed position of the earliest retained event whose
// corrected time is at or after the affected time. It returns -1 if no event
// is affected, meaning the late event is older than all retained history.
func (r *Replay) Earliest(affected model.Nanos) int {
	for i, e := range r.events {
		if e.CorrectedTime >= affected {
			return i
		}
	}
	return -1
}

// Replay returns the retained events at or after the affected time plus the
// provided late events, in canonical order. The returned slice is a fresh
// copy; the internal log is unchanged so replay is repeatable.
func (r *Replay) Replay(affected model.Nanos, late []Sample) ([]Sample, int) {
	idx := r.Earliest(affected)
	out := make([]Sample, 0, len(r.events)+len(late))
	if idx >= 0 {
		out = append(out, r.events[idx:]...)
	}
	out = append(out, late...)
	SortSamples(out)
	return out, len(out)
}

// AffectedSince returns the earliest corrected time retained.
func (r *Replay) AffectedSince() (model.Nanos, bool) {
	if len(r.events) == 0 {
		return 0, false
	}
	return r.events[0].CorrectedTime, true
}

// Len returns the number of retained events.
func (r *Replay) Len() int { return len(r.events) }

// Dropped returns how many events aged out of the window.
func (r *Replay) Dropped() int64 { return r.dropped }

// SortSamples orders samples deterministically by corrected time, then track,
// then extended sequence, then index. Applying it to the same multiset always
// yields the same sequence.
func SortSamples(s []Sample) {
	sort.SliceStable(s, func(i, j int) bool { return less(s[i], s[j]) })
}

// RecomputeDigest applies SortSamples and returns the canonical digest of the
// replayed events. This is the value reported as the late-recompute result.
func RecomputeDigest(s []Sample) string {
	SortSamples(s)
	return digestSamples(s)
}
