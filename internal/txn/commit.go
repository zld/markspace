package txn

import (
	"encoding/json"
	"os"
	"path/filepath"

	mediaerr "mediacheck/internal/errors"
)

// Checkpoint is the durable, atomically committed state of one pipeline run.
// It is written only after every referenced stage artifact has passed its
// hash check, and its replacement is a temp-file plus rename so a crash can
// never leave a torn checkpoint.
type Checkpoint struct {
	RunID        string            `json:"run_id"`
	InputSHA256  string            `json:"input_sha256"`
	Stages       []CommittedStage  `json:"stages"`
	SegmentHashes map[string]string `json:"segment_hashes"`
	CommittedAt  string            `json:"committed_at"`
}

// CommittedStage records one replayed/committed stage and the cursor reached.
type CommittedStage struct {
	Stage  string `json:"stage"`
	Track  string `json:"track,omitempty"`
	Cursor int64  `json:"cursor"`
	SHA    string `json:"output_sha256"`
}

const checkpointName = "checkpoint.json"

// Manager coordinates the WAL and the single atomic checkpoint file.
type Manager struct {
	dir string
	wal *WAL
}

// OpenManager opens the WAL and any existing checkpoint inside dir.
func OpenManager(dir string) (*Manager, error) {
	wal, err := OpenWAL(dir)
	if err != nil {
		return nil, err
	}
	return &Manager{dir: dir, wal: wal}, nil
}

// WAL exposes the underlying journal for stage appends/commits.
func (m *Manager) WAL() *WAL { return m.wal }

// HasCommitted reports whether any durable stage commit exists in this dir.
func (m *Manager) HasCommitted() bool { return m.wal.HasCommitted() }

// Finalize verifies every committed WAL artifact still hashes correctly,
// builds the checkpoint, and commits it atomically. Any verification failure
// aborts before the checkpoint is replaced and returns a recovery error.
func (m *Manager) Finalize(runID, inputSHA string,
	segments map[string]string, now string) (*Checkpoint, error) {
	committed := m.wal.CommittedEntries()
	stages := make([]CommittedStage, 0, len(committed))
	for _, e := range committed {
		if err := m.wal.verifyFinal(&e); err != nil {
			return nil, mediaerr.Wrap(err, mediaerr.StageTxn, e.Track,
				"pre-commit verification failed for %s", e.Stage)
		}
		stages = append(stages, CommittedStage{
			Stage: e.Stage, Track: e.Track,
			Cursor: e.Cursor, SHA: e.OutputSHA,
		})
	}
	cp := &Checkpoint{
		RunID: runID, InputSHA256: inputSHA,
		Stages: stages, SegmentHashes: segments, CommittedAt: now,
	}
	if err := m.writeAtomic(cp); err != nil {
		return nil, err
	}
	return cp, nil
}

// LoadCheckpoint reads and validates the committed checkpoint. A missing
// checkpoint is reported as (nil, nil) so a fresh run can proceed; a present
// but unparseable or internally inconsistent file is a hard recovery error.
func (m *Manager) LoadCheckpoint() (*Checkpoint, error) {
	path := filepath.Join(m.dir, checkpointName)
	data, err := os.ReadFile(path)
	if os.IsNotExist(err) {
		return nil, nil
	}
	if err != nil {
		return nil, mediaerr.Wrap(err, mediaerr.StageTxn, "", "read checkpoint")
	}
	var cp Checkpoint
	if err := json.Unmarshal(data, &cp); err != nil {
		return nil, mediaerr.NewCoded(mediaerr.StageTxn, "",
			"txn.checkpoint-corrupt", "checkpoint parse failed: %v", err)
	}
	// Cross-check the checkpoint against the committed WAL entries: every
	// stage the checkpoint claims must exist and hash-match in the journal.
	committed := m.wal.CommittedEntries()
	have := map[string]string{}
	for _, e := range committed {
		have[keyOf(e.Stage, e.Track)] = e.OutputSHA
	}
	for _, st := range cp.Stages {
		sha, ok := have[keyOf(st.Stage, st.Track)]
		if !ok {
			return nil, mediaerr.NewCoded(mediaerr.StageTxn, st.Track,
				"txn.recover-mismatch",
				"checkpoint references uncommitted stage %s", st.Stage)
		}
		if sha != st.SHA {
			return nil, mediaerr.NewCoded(mediaerr.StageTxn, st.Track,
				"txn.recover-mismatch", "stage %s hash diverged from WAL", st.Stage)
		}
	}
	return &cp, nil
}

// writeAtomic serializes to a temp file, fsyncs, then renames over the old
// checkpoint so readers never observe a partially written file.
func (m *Manager) writeAtomic(cp *Checkpoint) error {
	data, err := json.MarshalIndent(cp, "", "  ")
	if err != nil {
		return mediaerr.Wrap(err, mediaerr.StageTxn, "", "marshal checkpoint")
	}
	final := filepath.Join(m.dir, checkpointName)
	tmp := final + ".tmp"
	if err := os.WriteFile(tmp, data, 0o644); err != nil {
		return mediaerr.Wrap(err, mediaerr.StageTxn, "", "write temp checkpoint")
	}
	d, err := os.Open(m.dir)
	if err != nil {
		return mediaerr.Wrap(err, mediaerr.StageTxn, "", "open checkpoint dir")
	}
	if err := d.Sync(); err != nil {
		d.Close()
		return mediaerr.Wrap(err, mediaerr.StageTxn, "", "fsync temp checkpoint")
	}
	d.Close()
	if err := os.Rename(tmp, final); err != nil {
		return mediaerr.Wrap(err, mediaerr.StageTxn, "", "rename checkpoint")
	}
	d2, err := os.Open(m.dir)
	if err != nil {
		return mediaerr.Wrap(err, mediaerr.StageTxn, "", "open checkpoint dir")
	}
	defer d2.Close()
	if err := d2.Sync(); err != nil {
		return mediaerr.Wrap(err, mediaerr.StageTxn, "", "fsync checkpoint dir")
	}
	return nil
}

// DiscardUncommitted removes temp artifacts left by an interrupted run. The
// WAL replay already drops uncommitted records; this sweeps stray .tmp files.
func (m *Manager) DiscardUncommitted() ([]string, error) {
	entries, err := os.ReadDir(m.dir)
	if err != nil {
		return nil, mediaerr.Wrap(err, mediaerr.StageTxn, "", "sweep dir")
	}
	var removed []string
	for _, en := range entries {
		name := en.Name()
		if filepath.Ext(name) == ".tmp" {
			if err := os.Remove(filepath.Join(m.dir, name)); err != nil {
				return removed, mediaerr.Wrap(err, mediaerr.StageTxn, "",
					"remove uncommitted temp %s", name)
			}
			removed = append(removed, name)
		}
	}
	return removed, nil
}
