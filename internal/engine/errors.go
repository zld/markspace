package engine

import (
	"errors"

	"mediad/internal/dag"
	"mediad/internal/model"
)

// ErrInterrupted is returned when a run stops at the configured interrupt
// point. It is not a failure: the checkpoint already holds every committed
// segment, and a subsequent resume completes the run.
var ErrInterrupted = errors.New("engine: run interrupted at configured boundary")

// committedSegmentCount counts distinct segment indices in the ledger.
func committedSegmentCount(cp *dag.Checkpoint) int {
	seen := map[int]struct{}{}
	for _, c := range cp.Commits() {
		seen[c.Index] = struct{}{}
	}
	return len(seen)
}

func firstCorruptOffset(c *model.CorruptError) int64 {
	if c == nil {
		return 0
	}
	return c.Offset
}

func firstCorruptPID(c *model.CorruptError) uint16 {
	if c == nil {
		return 0
	}
	return c.PID
}
