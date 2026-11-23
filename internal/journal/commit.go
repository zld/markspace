package journal

import (
	"os"
	"path/filepath"

	"mediaroute/internal/clock"
)

// CrashHook is invoked between durable steps. Returning an error simulates a
// process crash at that named point; production callers pass nil.
type CrashHook func(point string) error

// Standard crash points used across the pipeline.
const (
	PointAfterWALBegin  = "wal-begin"
	PointAfterWALData   = "wal-data"
	PointAfterWALCommit = "wal-commit"
	PointSegmentCommit  = "segment-commit"
	PointIndexCommit    = "index-commit"
)

// Coordinator runs staged commits against one journal directory.
type Coordinator struct {
	dir  string
	wal  *WAL
	hook CrashHook
}

// OpenCoordinator creates the WAL and committed-segment layout.
func OpenCoordinator(dir string, hook CrashHook) (*Coordinator, error) {
	wal, err := CreateWAL(dir)
	if err != nil {
		return nil, err
	}
	if err := os.MkdirAll(filepath.Join(dir, segDir), 0o755); err != nil {
		return nil, clock.E(clock.PhaseJournal, 0, -1, true, "cannot create segment dir", err)
	}
	return &Coordinator{dir: dir, wal: wal, hook: hook}, nil
}

func (c *Coordinator) crash(point string) error {
	if c.hook == nil {
		return nil
	}
	if err := c.hook(point); err != nil {
		return clock.E(clock.PhaseJournal, 0, -1, true, "simulated crash at "+point, err)
	}
	return nil
}

// CommitStage persists artifacts for one named stage through the WAL and only
// then publishes them into the committed directory. A crash before the COMMIT
// record leaves nothing committed; a crash afterwards survives restart.
func (c *Coordinator) CommitStage(stage string, artifacts map[string][]byte) error {
	if err := c.wal.Append(Record{Kind: RecBegin, Key: stage}); err != nil {
		return err
	}
	if err := c.crash(PointAfterWALBegin + ":" + stage); err != nil {
		return err
	}
	// Stage payloads into a private temp directory first.
	stageTmp := filepath.Join(c.dir, walTmpName, stage)
	if err := os.MkdirAll(stageTmp, 0o755); err != nil {
		return clock.E(clock.PhaseJournal, 0, -1, true, "staging mkdir failed", err)
	}
	for key, val := range artifacts {
		if err := c.wal.Append(Record{Kind: RecData, Key: stage + "/" + key, Value: val}); err != nil {
			return err
		}
		path := filepath.Join(stageTmp, filepath.Base(key))
		if err := os.WriteFile(path, val, 0o644); err != nil {
			return clock.E(clock.PhaseJournal, 0, -1, true, "staging write failed", err)
		}
	}
	if err := c.crash(PointAfterWALData + ":" + stage); err != nil {
		return err
	}
	// COMMIT is the atomic decision point; it is fsynced inside Append.
	if err := c.wal.Append(Record{Kind: RecCommit, Key: stage}); err != nil {
		return err
	}
	if err := c.crash(PointAfterWALCommit + ":" + stage); err != nil {
		return err
	}
	// Publish staged files into the committed directory, then fsync the dir.
	dest := filepath.Join(c.dir, segDir, stage)
	if err := os.MkdirAll(dest, 0o755); err != nil {
		return clock.E(clock.PhaseJournal, 0, -1, true, "commit mkdir failed", err)
	}
	for key, val := range artifacts {
		if err := os.WriteFile(filepath.Join(dest, filepath.Base(key)), val, 0o644); err != nil {
			return clock.E(clock.PhaseJournal, 0, -1, true, "commit publish failed", err)
		}
	}
	if err := syncDir(filepath.Join(c.dir, segDir)); err != nil {
		return err
	}
	if err := os.RemoveAll(stageTmp); err != nil {
		return clock.E(clock.PhaseJournal, 0, -1, true, "staging cleanup failed", err)
	}
	if err := c.crash(PointSegmentCommit + ":" + stage); err != nil {
		return err
	}
	return nil
}

// Abort records an explicit rollback marker for a stage.
func (c *Coordinator) Abort(stage string) error {
	return c.wal.Append(Record{Kind: RecAbort, Key: stage})
}

// Close flushes the WAL handle.
func (c *Coordinator) Close() error { return c.wal.Close() }

// Dir exposes the journal root.
func (c *Coordinator) Dir() string { return c.dir }

// CommittedPath returns the path of a published stage artifact.
func (c *Coordinator) CommittedPath(stage, key string) string {
	return filepath.Join(c.dir, segDir, stage, filepath.Base(key))
}

// CommittedStages lists stage directories that hold at least one artifact.
func (c *Coordinator) CommittedStages() ([]string, error) {
	entries, err := os.ReadDir(filepath.Join(c.dir, segDir))
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, clock.E(clock.PhaseJournal, 0, -1, true, "cannot list committed stages", err)
	}
	var out []string
	for _, e := range entries {
		if e.IsDir() {
			out = append(out, e.Name())
		}
	}
	return out, nil
}

// RecoveryReport describes what restart found on disk.
type RecoveryReport struct {
	Replayed   []string           // stages whose COMMIT record was durable
	Isolated   bool               // an uncommitted/corrupt tail was cut off
	TailOffset int64              // byte offset the WAL was truncated to
	Artifacts  map[string][]byte  // replayed stage -> key -> payload
}

// Recover restores committed state from a journal directory after restart.
// Uncommitted stages (no COMMIT record) are discarded; a corrupt tail is
// isolated before replay.
func Recover(dir string) (*RecoveryReport, error) {
	recs, tail, err := ReadWAL(dir)
	report := &RecoveryReport{Artifacts: map[string][]byte{}, TailOffset: tail}
	if err != nil {
		// Corrupt or partial tail: quarantine everything past the good prefix.
		if isoErr := IsolateTail(dir, tail); isoErr != nil {
			return nil, isoErr
		}
		report.Isolated = true
	}
	committed := map[string]bool{}
	stageData := map[string]map[string][]byte{}
	for _, rec := range recs {
		switch rec.Kind {
		case RecBegin, RecAbort:
			// Begin alone never publishes; Abort explicitly cancels.
			if rec.Kind == RecAbort {
				delete(committed, rec.Key)
				delete(stageData, rec.Key)
			}
		case RecData:
			stage, key, ok := splitStageKey(rec.Key)
			if !ok {
				return nil, clock.E(clock.PhaseJournal, 0, -1, false, "malformed WAL data key", nil)
			}
			if stageData[stage] == nil {
				stageData[stage] = map[string][]byte{}
			}
			stageData[stage][key] = rec.Value
		case RecCommit:
			committed[rec.Key] = true
		}
	}
	// Remove any staged-but-uncommitted temp directories.
	if err := os.RemoveAll(filepath.Join(dir, walTmpName)); err != nil {
		return nil, clock.E(clock.PhaseJournal, 0, -1, true, "temp cleanup on recovery failed", err)
	}
	// Re-publish every committed stage from WAL data, making recovery the
	// single source of truth regardless of how far file publication got.
	if err := os.MkdirAll(filepath.Join(dir, segDir), 0o755); err != nil {
		return nil, clock.E(clock.PhaseJournal, 0, -1, true, "segment mkdir on recovery failed", err)
	}
	for stage := range committed {
		dest := filepath.Join(dir, segDir, stage)
		if err := os.MkdirAll(dest, 0o755); err != nil {
			return nil, clock.E(clock.PhaseJournal, 0, -1, true, "replay mkdir failed", err)
		}
		for key, val := range stageData[stage] {
			if err := os.WriteFile(filepath.Join(dest, filepath.Base(key)), val, 0o644); err != nil {
				return nil, clock.E(clock.PhaseJournal, 0, -1, true, "replay publish failed", err)
			}
			report.Artifacts[stage+"/"+key] = val
		}
		report.Replayed = append(report.Replayed, stage)
	}
	if err := syncDir(filepath.Join(dir, segDir)); err != nil {
		return nil, err
	}
	return report, nil
}

func splitStageKey(k string) (stage, key string, ok bool) {
	for i := 0; i < len(k); i++ {
		if k[i] == '/' {
			return k[:i], k[i+1:], true
		}
	}
	return "", "", false
}

func syncDir(path string) error {
	d, err := os.Open(path)
	if err != nil {
		return clock.E(clock.PhaseJournal, 0, -1, true, "dir open for fsync failed", err)
	}
	defer d.Close()
	if err := d.Sync(); err != nil {
		return clock.E(clock.PhaseJournal, 0, -1, true, "dir fsync failed", err)
	}
	return nil
}
