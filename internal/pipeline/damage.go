package pipeline

import (
	"os"
	"path/filepath"

	"mediad/internal/fault"
	"mediad/internal/index"
	"mediad/internal/recover"
	"mediad/internal/safeio"
)

// SimulateCrash truncates the live journal mid-document and removes the
// newest snapshot plus the segment files only the latest checkpoint covered.
// It models a failure during the final atomic commit: the previous snapshot
// remains the last verified recovery point.
func (e *Engine) SimulateCrash() error {
	journal, err := safeio.SafeJoin(e.cfg.OutputDir, index.Name)
	if err != nil {
		return err
	}
	raw, err := os.ReadFile(journal)
	if err != nil {
		return fault.Wrap(fault.CatWrite, "pipeline.crash", -1, -1, true, err)
	}
	if len(raw) < 2 {
		return fault.New(fault.CatCheckpoint, "pipeline.crash", true,
			"journal too small to simulate a crash")
	}
	// A partial write leaves a syntactically invalid JSON document behind.
	if err := os.WriteFile(journal, raw[:len(raw)/2], 0o644); err != nil {
		return fault.Wrap(fault.CatWrite, "pipeline.crash", -1, -1, true, err)
	}
	// Drop the newest snapshot so recovery must fall back to the prior one.
	if n, snapPath, err := index.LatestSnapshot(e.cfg.OutputDir); err == nil {
		_ = os.Remove(snapPath)
		// Remove segments this snapshot committed; earlier snapshots keep theirs.
		e.removeSnapshotSegments(n)
	}
	return nil
}

// removeSnapshotSegments deletes segment files whose ids belong to the given
// checkpoint batch, mirroring the redo work a real restart would perform.
func (e *Engine) removeSnapshotSegments(snapshotSeq int) {
	segDir, err := safeio.SafeJoin(e.cfg.OutputDir, "segments")
	if err != nil {
		return
	}
	entries, err := os.ReadDir(segDir)
	if err != nil {
		return
	}
	batchStart := (snapshotSeq - 1) * e.cfg.CheckpointEvery
	batchEnd := snapshotSeq * e.cfg.CheckpointEvery
	for _, ent := range entries {
		id, ok := parseSegmentID(ent.Name())
		if !ok {
			continue
		}
		if id >= batchStart && id < batchEnd {
			_ = os.Remove(filepath.Join(segDir, ent.Name()))
		}
	}
}

// RerunAfterCrash validates the damaged output directory and returns the
// recovery plan plus the count of chunks that must be reprocessed.
func (e *Engine) RerunAfterCrash() (*recover.Plan, int, error) {
	plan, err := recover.ValidateAndPlan(e.cfg.OutputDir)
	if err != nil {
		return nil, 0, err
	}
	if plan.LastGoodSegment >= 0 {
		ix, err := index.Load(e.cfg.OutputDir)
		if err != nil {
			return nil, 0, err
		}
		e.ix = ix
	} else {
		e.ix = index.New(e.cfg.SampleRate)
	}
	redo := e.cfg.Chunks - plan.ResumeAtChunk
	return plan, redo, nil
}
