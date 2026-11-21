// Package task implements the per-segment task state machine.
// Only the transitions listed in transitions are legal; any other move is an error.
package task

import (
	"fmt"

	"bitrack/internal/model"
)

// transitions is the adjacency list of legal state moves.
var transitions = map[model.SegmentState][]model.SegmentState{
	model.StateQueued:     {model.StateRunning, model.StatePaused, model.StateFailed},
	model.StateRunning:    {model.StateValidating, model.StateRetryable, model.StatePaused, model.StateFailed},
	model.StateValidating: {model.StateCommitted, model.StateRetryable, model.StateFailed},
	model.StateRetryable:  {model.StateQueued, model.StatePaused, model.StateFailed},
	model.StatePaused:     {model.StateQueued, model.StateFailed},
	model.StateCommitted:  {},
	model.StateFailed:     {model.StateQueued},
}

// CanTransition reports whether moving from -> to is allowed.
func CanTransition(from, to model.SegmentState) bool {
	for _, s := range transitions[from] {
		if s == to {
			return true
		}
	}
	return false
}

// Move applies a legal transition to seg, returning an error describing the
// illegal move otherwise. The error is locatable via the segment index.
func Move(seg *model.Segment, to model.SegmentState) error {
	if seg == nil {
		return fmt.Errorf("state machine: nil segment")
	}
	if seg.State == to {
		return nil
	}
	if !CanTransition(seg.State, to) {
		return fmt.Errorf("state machine: segment %d illegal transition %s -> %s",
			seg.Index, seg.State, to)
	}
	seg.State = to
	return nil
}

// Reopen resets a failed segment back to the queue. Only failed tasks may be
// manually reopened; committed output is never silently re-queued.
func Reopen(seg *model.Segment) error {
	if err := Move(seg, model.StateQueued); err != nil {
		return err
	}
	seg.LastError = ""
	return nil
}

// IsTerminal reports whether the state has no legal outgoing moves.
func IsTerminal(s model.SegmentState) bool {
	return len(transitions[s]) == 0
}
