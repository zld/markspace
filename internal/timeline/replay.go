package timeline

import (
	"sort"

	"mediapack/internal/model"
)

// replayWith handles a late event by restoring the nearest checkpoint,
// inserting the event into the correct Seq-ordered position and recomputing
// every boundary deterministically. The result must match a full
// recomputation from scratch over the same Seq-ordered event set.
func (t *Timeline) replayWith(late Event) (*model.Boundary, error) {
	t.ReplayCount++

	// Restore the most recent checkpoint whose events all precede the late
	// event in arrival order, then re-append everything that followed it.
	cpIdx := len(t.checkpoints) - 1
	if cpIdx < 0 {
		return t.fullRecompute(late)
	}
	cp := t.checkpoints[cpIdx]

	// Events committed after this checkpoint must be reprocessed after the
	// late event is merged in event-time order.
	tail := append([]Event(nil), t.events[len(cp.stableEvents):]...)

	ordered := make([]Event, 0, len(cp.stableEvents)+len(tail)+1)
	ordered = append(ordered, cp.stableEvents...)
	ordered = append(ordered, tail...)
	ordered = insertByTime(ordered, late)

	return t.rebuild(ordered, late)
}

// fullRecompute is the fallback when no checkpoint exists yet; it is also the
// reference implementation that tests compare replay results against.
func (t *Timeline) fullRecompute(late Event) (*model.Boundary, error) {
	ordered := insertByTime(append([]Event(nil), t.events...), late)
	return t.rebuild(ordered, late)
}

// rebuild resets all derived state and reprocesses the complete Seq-ordered
// event set, returning the boundary that corresponds to the late event.
func (t *Timeline) rebuild(ordered []Event, late Event) (*model.Boundary, error) {
	fresh := New(t.rule, t.checkpointGap)
	var lateBoundary *model.Boundary
	for _, e := range ordered {
		b, err := fresh.process(e)
		if err != nil {
			return nil, err
		}
		fresh.seen[e.key()] = e.Seq
		fresh.events = append(fresh.events, e)
		fresh.boundaries = append(fresh.boundaries, *b)
		fresh.advanceWatermark(b.RepairedTick)
		if e.key() == late.key() {
			bb := *b
			bb.Repair = tagLate(bb.Repair)
			lateBoundary = &bb
		}
	}
	// Commit the recomputed state atomically; a failure above leaves the
	// original timeline untouched (no half-applied result).
	t.watermark = fresh.watermark
	t.tracks = fresh.tracks
	t.seen = fresh.seen
	t.boundaries = fresh.boundaries
	t.events = fresh.events
	t.checkpoints = fresh.checkpoints
	t.WrapCount = fresh.WrapCount
	t.OverlapCount = fresh.OverlapCount
	t.GapCount = fresh.GapCount
	if lateBoundary == nil {
		return nil, model.NewError(model.CodeLateBoundary,
			"late boundary not found after replay").
			WithFile(late.Source).WithTrack(late.TrackID).WithTick(late.OriginalTick)
	}
	return lateBoundary, nil
}

// FullRecomputeFromScratch is exported for tests: process every event in
// event-time order on a clean timeline and return the resulting boundaries.
// This is the oracle the replay path must match.
func FullRecomputeFromScratch(rule Rule, checkpointGap int, events []Event,
) ([]model.Boundary, error) {
	sorted := append([]Event(nil), events...)
	sort.SliceStable(sorted, func(i, j int) bool {
		if sorted[i].OriginalTick != sorted[j].OriginalTick {
			return sorted[i].OriginalTick < sorted[j].OriginalTick
		}
		return sorted[i].Seq < sorted[j].Seq
	})
	fresh := New(rule, checkpointGap)
	for _, e := range sorted {
		b, err := fresh.process(e)
		if err != nil {
			return nil, err
		}
		fresh.seen[e.key()] = e.Seq
		fresh.events = append(fresh.events, e)
		fresh.boundaries = append(fresh.boundaries, *b)
		fresh.advanceWatermark(b.RepairedTick)
	}
	return fresh.Boundaries(), nil
}

// insertByTime places e into the slice by (OriginalTick, Seq), matching the
// ordering used by FullRecomputeFromScratch.
func insertByTime(events []Event, e Event) []Event {
	pos := sort.Search(len(events), func(i int) bool {
		if events[i].OriginalTick != e.OriginalTick {
			return events[i].OriginalTick >= e.OriginalTick
		}
		return events[i].Seq >= e.Seq
	})
	events = append(events, Event{})
	copy(events[pos+1:], events[pos:])
	events[pos] = e
	return events
}

func tagLate(repair string) string {
	if repair == RepairNone {
		return RepairLate
	}
	return repair + "+" + RepairLate
}
