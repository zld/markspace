package job

import (
	"database/sql"
)

// Checkpoint is the durable resume marker for one task/phase. LastSample is
// the count of fully processed samples, so the next run begins at that index.
type Checkpoint struct {
	TaskKey    string
	Phase      Phase
	LastSample int64
	UpdatedAt  int64
}

// SaveCheckpoint upserts the marker for a phase and simultaneously advances
// the task's sample progress. The write happens inside one transaction so a
// crash cannot leave the checkpoint ahead of the task row (or vice versa).
func (s *Store) SaveCheckpoint(cp Checkpoint) error {
	tx, err := s.db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if _, err := tx.Exec(`INSERT INTO checkpoints (task_key, phase, last_sample, updated_at)
		VALUES (?, ?, ?, ?)
		ON CONFLICT(task_key, phase) DO UPDATE SET
			last_sample = excluded.last_sample,
			updated_at = excluded.updated_at`,
		cp.TaskKey, string(cp.Phase), cp.LastSample, cp.UpdatedAt); err != nil {
		return err
	}
	if _, err := tx.Exec(`UPDATE tasks SET last_sample = ? WHERE key = ?`,
		cp.LastSample, cp.TaskKey); err != nil {
		return err
	}
	return tx.Commit()
}

// LatestCheckpoint returns the highest sample count recorded across all
// phases for a task. A missing row yields (0, nil) so fresh runs start at
// sample zero.
func (s *Store) LatestCheckpoint(taskKey string) (int64, error) {
	var last sql.NullInt64
	err := s.db.QueryRow(
		`SELECT MAX(last_sample) FROM checkpoints WHERE task_key = ?`, taskKey).
		Scan(&last)
	if err != nil {
		return 0, err
	}
	if !last.Valid {
		return 0, nil
	}
	return last.Int64, nil
}

// PhaseCheckpoint returns the marker stored for one phase, or (0, false) if
// that phase has never checkpointed.
func (s *Store) PhaseCheckpoint(taskKey string, phase Phase) (int64, bool, error) {
	var last int64
	err := s.db.QueryRow(
		`SELECT last_sample FROM checkpoints WHERE task_key = ? AND phase = ?`,
		taskKey, string(phase)).Scan(&last)
	if err == sql.ErrNoRows {
		return 0, false, nil
	}
	if err != nil {
		return 0, false, err
	}
	return last, true, nil
}

// RecordObject remembers an archived object so recovery can re-verify it and
// manifest publication can enumerate entries without scanning the disk.
func (s *Store) RecordObject(taskKey, trackKey string, sampleIdx int, hash string, size int64) error {
	_, err := s.db.Exec(`INSERT INTO objects (task_key, track_key, sample_idx, object_hash, size)
		VALUES (?, ?, ?, ?, ?)
		ON CONFLICT(task_key, track_key, sample_idx) DO UPDATE SET
			object_hash = excluded.object_hash, size = excluded.size`,
		taskKey, trackKey, sampleIdx, hash, size)
	return err
}

// ObjectExists reports whether an object was already archived for a sample.
func (s *Store) ObjectExists(taskKey, trackKey string, sampleIdx int) (bool, string, error) {
	var hash string
	err := s.db.QueryRow(
		`SELECT object_hash FROM objects WHERE task_key = ? AND track_key = ? AND sample_idx = ?`,
		taskKey, trackKey, sampleIdx).Scan(&hash)
	if err == sql.ErrNoRows {
		return false, "", nil
	}
	if err != nil {
		return false, "", err
	}
	return true, hash, nil
}

// Objects returns every recorded object for a task in canonical key order.
func (s *Store) Objects(taskKey string) ([]manifestObject, error) {
	rows, err := s.db.Query(
		`SELECT track_key, sample_idx, object_hash, size FROM objects
		WHERE task_key = ? ORDER BY track_key, sample_idx`, taskKey)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []manifestObject
	for rows.Next() {
		var mo manifestObject
		if err := rows.Scan(&mo.TrackKey, &mo.SampleIdx, &mo.Hash, &mo.Size); err != nil {
			return nil, err
		}
		out = append(out, mo)
	}
	return out, rows.Err()
}

// manifestObject is the storage-layer projection of a manifest entry.
type manifestObject struct {
	TrackKey  string
	SampleIdx int
	Hash      string
	Size      int64
}
