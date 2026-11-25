package timeline

import (
	"fmt"

	"archive.example/mediakernel/internal/events"
)

// checkpoint is a restorable projection of the committed prefix. It stores the
// prefix length and the committed events up to that point, so a rollback can
// detect exactly which outputs were replaced.
type checkpoint struct {
	prefixLen int
	events    []events.Event
	watermark int64
}

// takeCheckpointIfDue records a checkpoint whenever the committed prefix has
// advanced by the configured interval since the last one.
func (t *Timeline) takeCheckpointIfDue() *events.Error {
	if len(t.checkpoints) > 0 && t.committedPrefix-t.checkpoints[len(t.checkpoints)-1].prefixLen < t.cfg.CheckpointEvery {
		return nil
	}
	t.pushCheckpoint(t.committedPrefix)
	return nil
}

func (t *Timeline) pushCheckpoint(prefixLen int) {
	cp := checkpoint{
		prefixLen: prefixLen,
		events:    append([]events.Event(nil), t.accepted[:prefixLen]...),
		watermark: t.watermark,
	}
	t.checkpoints = append(t.checkpoints, cp)
	if len(t.checkpoints) > t.cfg.MaxCheckpoints {
		t.checkpoints = append([]checkpoint(nil), t.checkpoints[len(t.checkpoints)-t.cfg.MaxCheckpoints:]...)
	}
}

// latestCheckpointAtOrBefore returns the newest checkpoint whose committed
// prefix ends at or before index. Its contents are unaffected by an insertion
// at index, so it is a valid rollback point.
func (t *Timeline) latestCheckpointAtOrBefore(index int) (checkpoint, bool) {
	for i := len(t.checkpoints) - 1; i >= 0; i-- {
		if t.checkpoints[i].prefixLen <= index {
			return t.checkpoints[i], true
		}
	}
	return checkpoint{}, false
}

// replayLate inserts an event whose canonical position lies inside the
// committed prefix. It rolls back to the nearest checkpoint, recomputes the
// committed projection there and reports which outputs were replaced. The
// recomputation is a pure function of the accepted log, so a late event
// produces the same final state as processing the complete ordered input.
func (t *Timeline) replayLate(e events.Event, index int) (AdmitResult, *events.Error) {
	oldPrefix := t.committedPrefix
	cp, ok := t.latestCheckpointAtOrBefore(index)
	if !ok {
		// With no usable checkpoint the rollback would start at zero; that is
		// the whole committed prefix and must obey the window limit too.
		if oldPrefix > t.cfg.MaxReplayEvents {
			return AdmitResult{}, events.NewError(events.CodeReplayWindow, e.Track, e.Seq, -1, "replay",
				fmt.Sprintf("no checkpoint before index %d; rollback of %d events exceeds limit %d",
					index, oldPrefix, t.cfg.MaxReplayEvents), nil)
		}
		cp = checkpoint{prefixLen: 0, watermark: t.watermark}
	}
	if oldPrefix-cp.prefixLen > t.cfg.MaxReplayEvents {
		return AdmitResult{}, events.NewError(events.CodeReplayWindow, e.Track, e.Seq, -1, "replay",
			fmt.Sprintf("rollback of %d events exceeds limit %d", oldPrefix-cp.prefixLen, t.cfg.MaxReplayEvents), nil)
	}

	// Snapshot the region the rollback will recompute, then insert.
	before := append([]events.Event(nil), t.accepted[cp.prefixLen:oldPrefix]...)
	t.insertAt(index, e)

	// Recompute the committed prefix from the checkpoint with the unchanged
	// watermark. All events in [cp.prefixLen, ...) are re-evaluated, including
	// the inserted one and the events that used to be final.
	t.committedPrefix = cp.prefixLen
	_ = t.commitReplay()

	after := t.accepted[cp.prefixLen:t.committedPrefix]
	var replaced []events.Event
	for j, ev := range after {
		if j >= len(before) || events.CompareEvents(before[j], ev) != 0 {
			replaced = append(replaced, ev)
		}
	}
	// Invalidate checkpoints taken inside the recomputed region: they refer
	// to a committed prefix that included the old ordering.
	kept := t.checkpoints[:0]
	for _, c := range t.checkpoints {
		if c.prefixLen <= cp.prefixLen {
			kept = append(kept, c)
		}
	}
	t.checkpoints = kept
	t.pushCheckpoint(t.committedPrefix)

	t.stats.Replays++
	t.stats.Late++
	return AdmitResult{Replayed: replaced, Late: true, Replay: true}, nil
}

// commitReplay advances the prefix after a rollback. It deliberately does not
// take checkpoints; replayLate re-establishes them once the region is final.
func (t *Timeline) commitReplay() *events.Error {
	for t.committedPrefix < len(t.accepted) && t.accepted[t.committedPrefix].At <= t.watermark {
		t.committedPrefix++
	}
	if pending := len(t.accepted) - t.committedPrefix; pending > t.cfg.MaxPending {
		return events.NewError(events.CodeCapacity, "", 0, -1, "replay",
			fmt.Sprintf("pending limit %d reached (%d buffered)", t.cfg.MaxPending, pending), nil)
	}
	return nil
}

// ReplayStats exposes the checkpoint inventory for terminal summaries.
func (t *Timeline) ReplayStats() (checkpoints int, newestPrefix int, oldestPrefix int) {
	if len(t.checkpoints) == 0 {
		return 0, 0, 0
	}
	return len(t.checkpoints), t.checkpoints[len(t.checkpoints)-1].prefixLen, t.checkpoints[0].prefixLen
}
