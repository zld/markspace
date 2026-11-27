// Package pipeline runs the asset-processing work as an ordered sequence of
// named stages with durable, atomic state transitions. A stage's state is only
// committed after its artifacts are on disk and verified; an interrupted run
// leaves uncommitted temporary artifacts that are cleaned up before the stage
// is retried, while already-committed stages keep identical digests on rerun.
package pipeline

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"sync"

	"mediad/internal/model"
)

// Stage names used across the pipeline.
const (
	StageIngest    = "ingest"
	StageNormalize = "normalize"
	StageDerive    = "derive"
	StageManifest  = "manifest"
	StageArchive   = "archive"
)

// OrderedStages is the canonical execution order.
var OrderedStages = []string{StageIngest, StageNormalize, StageDerive, StageManifest, StageArchive}

// StageState is the durable status of one stage.
type StageState string

const (
	// StatePending means the stage has never run.
	StatePending StageState = "pending"
	// StateRunning means a stage directory exists but no commit record does;
	// any such record after a restart is treated as interrupted and cleaned.
	StateRunning StageState = "running"
	// StateCommitted means artifacts were written and verified.
	StateCommitted StageState = "committed"
)

// StageRecord is the persisted record for one committed stage.
type StageRecord struct {
	Name        string            `json:"name"`
	State       StageState        `json:"state"`
	SourceSHA   string            `json:"source_sha256"`
	Artifacts   map[string]string `json:"artifacts"` // logical name -> sha256
	Outputs     []string          `json:"outputs"`   // relative artifact file names
	Attempts    int               `json:"attempts"`
	CommittedAt string            `json:"committed_at"` // deterministic logical timestamp (sequence number)
}

// State is the durable per-source stage state document.
type State struct {
	SourceSHA string                  `json:"source_sha256"`
	Codec     string                  `json:"codec"`
	Stages    map[string]*StageRecord `json:"stages"`
	// Seq is a monotonic logical clock used for CommittedAt, avoiding wall
	// clock nondeterminism.
	Seq int `json:"seq"`
}

// Engine coordinates stage execution for one work directory. It is safe for
// concurrent use across different sources; per-source state has its own lock.
type Engine struct {
	root string
	mu   sync.Mutex // guards the state-file directory map
}

// NewEngine creates an engine rooted at workRoot.
func NewEngine(workRoot string) (*Engine, error) {
	if err := os.MkdirAll(filepath.Join(workRoot, "stages"), 0o755); err != nil {
		return nil, err
	}
	return &Engine{root: workRoot}, nil
}

func (e *Engine) statePath(sourceSHA string) string {
	return filepath.Join(e.root, "stages", sourceSHA+".json")
}

// StageDir returns the working directory for a stage; artifacts are written
// here before commit and moved into the committed area on success.
func (e *Engine) StageDir(sourceSHA, stage string) string {
	return filepath.Join(e.root, "stages", sourceSHA, stage)
}

// CommittedDir holds verified artifacts for a committed stage.
func (e *Engine) CommittedDir(sourceSHA, stage string) string {
	return filepath.Join(e.root, "stages", sourceSHA, stage+".committed")
}

// LoadState reads the durable state, returning a fresh pending state when no
// record exists yet.
func (e *Engine) LoadState(sourceSHA, codec string) (*State, error) {
	b, err := os.ReadFile(e.statePath(sourceSHA))
	if os.IsNotExist(err) {
		st := &State{SourceSHA: sourceSHA, Codec: codec, Stages: map[string]*StageRecord{}}
		for _, name := range OrderedStages {
			st.Stages[name] = &StageRecord{Name: name, State: StatePending, Artifacts: map[string]string{}}
		}
		return st, nil
	}
	if err != nil {
		return nil, err
	}
	st := &State{Stages: map[string]*StageRecord{}}
	if err := json.Unmarshal(b, st); err != nil {
		return nil, model.New(model.ErrStageState, codec, -1, "stage state parse failed: %v", err)
	}
	return st, nil
}

// saveState persists state atomically. Callers must hold the engine lock for
// the relevant source; this function takes the global lock to serialize file
// replacement.
func (e *Engine) saveState(st *State) error {
	e.mu.Lock()
	defer e.mu.Unlock()
	dir := filepath.Join(e.root, "stages")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	final := e.statePath(st.SourceSHA)
	tmp, err := os.CreateTemp(dir, ".state-*")
	if err != nil {
		return err
	}
	tmpName := tmp.Name()
	enc := json.NewEncoder(tmp)
	if err := enc.Encode(st); err != nil {
		_ = tmp.Close()
		_ = os.Remove(tmpName)
		return err
	}
	if err := tmp.Sync(); err != nil {
		_ = tmp.Close()
		_ = os.Remove(tmpName)
		return err
	}
	if err := tmp.Close(); err != nil {
		_ = os.Remove(tmpName)
		return err
	}
	return os.Rename(tmpName, final)
}

// IsCommitted reports whether a stage already has a verified commit.
func (st *State) IsCommitted(stage string) bool {
	r, ok := st.Stages[stage]
	return ok && r.State == StateCommitted
}

// LastCommitted returns the name of the last committed stage in canonical
// order, or "" when nothing is committed yet.
func (st *State) LastCommitted() string {
	last := ""
	for _, name := range OrderedStages {
		if st.IsCommitted(name) {
			last = name
		}
	}
	return last
}

// NextPending returns the first stage that is not committed.
func (st *State) NextPending() string {
	for _, name := range OrderedStages {
		if !st.IsCommitted(name) {
			return name
		}
	}
	return ""
}

// artifactDigest computes the SHA-256 of a file using the shared bounded
// hashing helper indirectly via a local implementation to avoid an import
// cycle; files here are small verified stage outputs.
func artifactDigest(path string) (string, error) {
	return digestFile(path)
}

// Prepare cleans any uncommitted leftovers from an earlier interrupted attempt
// and creates a fresh stage working directory. It must be called before every
// stage execution, including retries.
func (e *Engine) Prepare(st *State, stage string) (string, error) {
	rec := st.Stages[stage]
	if rec == nil {
		return "", model.New(model.ErrStageState, st.Codec, -1, "unknown stage %q", stage)
	}
	if rec.State == StateCommitted {
		return "", model.New(model.ErrStageState, st.Codec, -1,
			"stage %q already committed; refusing to re-run", stage)
	}
	dir := e.StageDir(st.SourceSHA, stage)
	// Remove every uncommitted leftover from an interrupted attempt.
	if err := os.RemoveAll(dir); err != nil {
		return "", model.New(model.ErrUncommittedCleanup, st.Codec, -1,
			"cannot clean uncommitted stage dir %s: %v", dir, err)
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return "", err
	}
	rec.State = StateRunning
	rec.Attempts++
	rec.Artifacts = map[string]string{}
	rec.Outputs = nil
	if err := e.saveState(st); err != nil {
		return "", err
	}
	return dir, nil
}

// Commit verifies every artifact in the stage directory, records their
// digests, moves the directory to the committed area and persists state. Only
// after this returns is the stage considered durable.
func (e *Engine) Commit(st *State, stage string) (*StageRecord, error) {
	rec := st.Stages[stage]
	if rec == nil || rec.State != StateRunning {
		return nil, model.New(model.ErrStageState, st.Codec, -1,
			"stage %q is not running, cannot commit", stage)
	}
	dir := e.StageDir(st.SourceSHA, stage)
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil, err
	}
	// Deterministic output order for stable records.
	names := make([]string, 0, len(entries))
	for _, en := range entries {
		if !en.IsDir() {
			names = append(names, en.Name())
		}
	}
	sort.Strings(names)
	if len(names) == 0 {
		return nil, model.New(model.ErrStageState, st.Codec, -1,
			"stage %q produced no artifacts", stage)
	}
	artifacts := make(map[string]string, len(names))
	for _, name := range names {
		sum, err := artifactDigest(filepath.Join(dir, name))
		if err != nil {
			return nil, err
		}
		artifacts[name] = sum
	}

	committed := e.CommittedDir(st.SourceSHA, stage)
	if err := os.RemoveAll(committed); err != nil { // remove stale committed copy
		return nil, err
	}
	if err := os.Rename(dir, committed); err != nil {
		return nil, err
	}
	st.Seq++
	rec.State = StateCommitted
	rec.Artifacts = artifacts
	rec.Outputs = names
	rec.CommittedAt = fmt.Sprintf("seq-%06d", st.Seq)
	if err := e.saveState(st); err != nil {
		return nil, err
	}
	return rec, nil
}

// VerifyCommitted re-hashes every artifact of a committed stage and compares
// it against the recorded digest. It is used by restore and by reruns.
func (e *Engine) VerifyCommitted(st *State, stage string) error {
	rec := st.Stages[stage]
	if rec == nil || rec.State != StateCommitted {
		return model.New(model.ErrStageState, st.Codec, -1, "stage %q is not committed", stage)
	}
	dir := e.CommittedDir(st.SourceSHA, stage)
	for name, want := range rec.Artifacts {
		got, err := artifactDigest(filepath.Join(dir, name))
		if err != nil {
			return model.New(model.ErrDigestMismatch, st.Codec, -1,
				"artifact %s unreadable: %v", name, err)
		}
		if got != want {
			return model.New(model.ErrDigestMismatch, st.Codec, -1,
				"artifact %s digest changed: recorded %s got %s", name, want, got)
		}
	}
	return nil
}
