package timeline

import "sort"

// RecomputeRange is one deterministic backtrack interval. The same set of
// events always produces the same start/end and the same event ordering.
type RecomputeRange struct {
	TrackID       uint32
	Start         uint64
	End           uint64
	AffectedCount int
	// EventKeys are the InputOrder values whose events fall in [Start,End].
	EventKeys []int
}

// Recomputer re-sorts the events inside an affected interval. It never
// decodes media; it only re-establishes deterministic event order after a
// late fragment inserts an event behind the watermark.
type Recomputer struct {
	trackID uint32
	// events keyed by InputOrder; append-only and bounded by window.
	events map[int]*SampleEvent
}

// NewRecomputer builds a Recomputer for one track.
func NewRecomputer(trackID uint32) *Recomputer {
	return &Recomputer{trackID: trackID, events: make(map[int]*SampleEvent)}
}

// Add registers an accepted event. Re-adding the same InputOrder is a
// no-op so duplicate runs are idempotent.
func (r *Recomputer) Add(ev *SampleEvent) {
	if _, ok := r.events[ev.InputOrder]; ok {
		return
	}
	r.events[ev.InputOrder] = ev
}

// Recompute deterministically rebuilds the ordering inside d's interval.
// Ordering keys are, in order: corrected DTS, then InputOrder. Returned
// events are the affected events in their new order plus the range.
func (r *Recomputer) Recompute(d LateDecision) ([]*SampleEvent, RecomputeRange) {
	var affected []*SampleEvent
	for _, ev := range r.events {
		if ev.TrackID != d.TrackID {
			continue
		}
		if ev.CorrectedDTS >= d.AffectedStart && ev.CorrectedDTS <= d.AffectedEnd {
			affected = append(affected, ev)
		}
	}
	sort.SliceStable(affected, func(i, j int) bool {
		if affected[i].CorrectedDTS != affected[j].CorrectedDTS {
			return affected[i].CorrectedDTS < affected[j].CorrectedDTS
		}
		return affected[i].InputOrder < affected[j].InputOrder
	})
	keys := make([]int, len(affected))
	for i, ev := range affected {
		keys[i] = ev.InputOrder
	}
	return affected, RecomputeRange{
		TrackID:       d.TrackID,
		Start:         d.AffectedStart,
		End:           d.AffectedEnd,
		AffectedCount: len(affected),
		EventKeys:     keys,
	}
}

// OrderedEvents returns every registered event in the same deterministic
// order Recompute uses, regardless of arrival order.
func (r *Recomputer) OrderedEvents() []*SampleEvent {
	out := make([]*SampleEvent, 0, len(r.events))
	for _, ev := range r.events {
		out = append(out, ev)
	}
	sort.SliceStable(out, func(i, j int) bool {
		if out[i].CorrectedDTS != out[j].CorrectedDTS {
			return out[i].CorrectedDTS < out[j].CorrectedDTS
		}
		return out[i].InputOrder < out[j].InputOrder
	})
	return out
}
