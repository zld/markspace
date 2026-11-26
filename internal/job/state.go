// Package job drives the state machine, per-phase checkpoints, content
// addressed archive and crash recovery. State persists in a SQLite database
// so a stopped run resumes from the first unprocessed sample.
package job

import (
	"database/sql"
	"fmt"

	_ "modernc.org/sqlite"
)

// Phase is one state-machine stage. The strings are stored verbatim and
// mirror the user-facing stage names.
type Phase string

const (
	PhaseQueued      Phase = "queued"
	PhaseScan        Phase = "scan"
	PhaseFingerprint Phase = "fingerprint"
	PhasePlan        Phase = "plan"
	PhaseArchive     Phase = "archive"
	PhaseComplete    Phase = "complete"
)

// phaseOrder defines the only legal forward transitions.
var phaseOrder = []Phase{
	PhaseQueued, PhaseScan, PhaseFingerprint, PhasePlan, PhaseArchive, PhaseComplete,
}

// CanTransition reports whether moving from -> to is legal. Phases advance
// one step at a time; recovery may reload the last completed phase but never
// moves it backwards in the table.
func CanTransition(from, to Phase) bool {
	fi := indexOf(from)
	ti := indexOf(to)
	return fi >= 0 && ti == fi+1
}

func indexOf(p Phase) int {
	for i, q := range phaseOrder {
		if q == p {
			return i
		}
	}
	return -1
}

// Task is the persisted state of one processing job.
type Task struct {
	Key            string
	SourceDigest   string
	PolicyDigest   string
	Phase          Phase
	LastSample     int64 // highest fully processed sample number (0-based count)
	TotalSamples   int64
	ManifestDigest string
	FailureStage   string
	FailureOffset  int64
	FailureReason  string
}

// Store wraps the SQLite connection and owns the schema.
type Store struct {
	db *sql.DB
}

// OpenStore opens (creating if needed) the state database at path.
func OpenStore(path string) (*Store, error) {
	db, err := sql.Open("sqlite", path+"?_pragma=busy_timeout(5000)&_pragma=journal_mode(WAL)")
	if err != nil {
		return nil, err
	}
	s := &Store{db: db}
	if err := s.migrate(); err != nil {
		db.Close()
		return nil, err
	}
	return s, nil
}

// Close releases the database handle.
func (s *Store) Close() error { return s.db.Close() }

func (s *Store) migrate() error {
	stmts := []string{
		`CREATE TABLE IF NOT EXISTS tasks (
			key TEXT PRIMARY KEY,
			source_digest TEXT NOT NULL,
			policy_digest TEXT NOT NULL,
			phase TEXT NOT NULL,
			last_sample INTEGER NOT NULL DEFAULT 0,
			total_samples INTEGER NOT NULL DEFAULT 0,
			manifest_digest TEXT NOT NULL DEFAULT '',
			failure_stage TEXT NOT NULL DEFAULT '',
			failure_offset INTEGER NOT NULL DEFAULT -1,
			failure_reason TEXT NOT NULL DEFAULT ''
		)`,
		`CREATE TABLE IF NOT EXISTS checkpoints (
			task_key TEXT NOT NULL,
			phase TEXT NOT NULL,
			last_sample INTEGER NOT NULL,
			updated_at INTEGER NOT NULL,
			PRIMARY KEY (task_key, phase)
		)`,
		`CREATE TABLE IF NOT EXISTS objects (
			task_key TEXT NOT NULL,
			track_key TEXT NOT NULL,
			sample_idx INTEGER NOT NULL,
			object_hash TEXT NOT NULL,
			size INTEGER NOT NULL,
			PRIMARY KEY (task_key, track_key, sample_idx)
		)`,
	}
	for _, q := range stmts {
		if _, err := s.db.Exec(q); err != nil {
			return fmt.Errorf("migrate: %w", err)
		}
	}
	return nil
}

// UpsertTask inserts a new task or returns the existing one for the same
// stable key. The boolean reports whether the row already existed, which the
// runner uses to short-circuit duplicate submissions.
func (s *Store) UpsertTask(t *Task) (existed bool, err error) {
	row := s.db.QueryRow(`SELECT phase FROM tasks WHERE key = ?`, t.Key)
	var phase string
	switch err := row.Scan(&phase); err {
	case nil:
		t.Phase = Phase(phase)
		return true, nil
	case sql.ErrNoRows:
	default:
		return false, err
	}
	_, err = s.db.Exec(`INSERT INTO tasks
		(key, source_digest, policy_digest, phase, last_sample, total_samples, failure_offset)
		VALUES (?, ?, ?, ?, 0, ?, -1)`,
		t.Key, t.SourceDigest, t.PolicyDigest, string(PhaseQueued), t.TotalSamples)
	if err != nil {
		return false, err
	}
	t.Phase = PhaseQueued
	return false, nil
}

// GetTask loads a task by key.
func (s *Store) GetTask(key string) (*Task, error) {
	row := s.db.QueryRow(`SELECT key, source_digest, policy_digest, phase,
		last_sample, total_samples, manifest_digest, failure_stage, failure_offset, failure_reason
		FROM tasks WHERE key = ?`, key)
	var t Task
	if err := row.Scan(&t.Key, &t.SourceDigest, &t.PolicyDigest, &t.Phase,
		&t.LastSample, &t.TotalSamples, &t.ManifestDigest,
		&t.FailureStage, &t.FailureOffset, &t.FailureReason); err != nil {
		return nil, err
	}
	return &t, nil
}

// Advance moves a task to the next phase, enforcing the legal transition.
func (s *Store) Advance(key string, from, to Phase) error {
	if !CanTransition(from, to) {
		return fmt.Errorf("illegal transition %s -> %s for task %s", from, to, key)
	}
	res, err := s.db.Exec(`UPDATE tasks SET phase = ? WHERE key = ? AND phase = ?`,
		string(to), key, string(from))
	if err != nil {
		return err
	}
	n, _ := res.RowsAffected()
	if n != 1 {
		return fmt.Errorf("task %s not in expected phase %s", key, from)
	}
	return nil
}

// RecordFailure stores a structured failure without changing the phase, so
// the last completed phase remains the resume point.
func (s *Store) RecordFailure(key string, stage string, offset int64, reason string) error {
	_, err := s.db.Exec(`UPDATE tasks SET failure_stage = ?, failure_offset = ?,
		failure_reason = ? WHERE key = ?`, stage, offset, reason, key)
	return err
}

// SetManifest records the final manifest digest once publication succeeds.
func (s *Store) SetManifest(key, digest string) error {
	_, err := s.db.Exec(`UPDATE tasks SET manifest_digest = ? WHERE key = ?`, digest, key)
	return err
}
