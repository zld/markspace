package job

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"regress/internal/model"
)

// ArtifactRecord identifies one produced artifact by its stable task key and
// its content hash, and tracks whether it has been committed to the manifest.
type ArtifactRecord struct {
	Key       string `json:"key"`
	RelPath   string `json:"rel_path"`
	Sha256    string `json:"sha256"`
	Size      int64  `json:"size"`
	Written   bool   `json:"written"`
	Committed bool   `json:"committed"`
}

// State is the durable, JSON-serialized processing state. It deliberately
// contains no wall-clock fields: a revision counter records checkpoint
// progression so reruns of identical inputs stay byte-deterministic.
type State struct {
	TaskKey   string           `json:"task_key"`
	InputHash string           `json:"input_hash"`
	Stage     model.Stage      `json:"stage"`
	Revision  int              `json:"revision"`
	Attempts  int              `json:"attempts"`
	Artifacts []ArtifactRecord `json:"artifacts"`
	// Published holds manifest entry keys in commit order; a key present
	// here must never be published again on recovery.
	Published []string     `json:"published"`
	LastError *model.Error `json:"last_error,omitempty"`
}

// Store owns the on-disk job hierarchy under one confined working directory.
type Store struct{ root string }

// Job is one resumable processing instance backed by its checkpoint file.
type Job struct {
	store *Store
	dir   string
	State *State
}

// Recovery summarizes what a resumed run found on disk.
type Recovery struct {
	// Recovered is true when a prior checkpoint existed and verified.
	Recovered bool
	// Republished counts artifact writes skipped because the artifact was
	// already present and hash-verified; the pipeline must instead commit
	// the missing manifest entries.
	VerifiedArtifacts int
}

// OpenStore creates (or reopens) the job store rooted at root. Every later
// path operation is confined beneath root.
func OpenStore(root string) (*Store, error) {
	abs, err := filepath.Abs(root)
	if err != nil {
		return nil, model.NewError(model.StageReceive, "store-root", root, "", 0, err)
	}
	if err := os.MkdirAll(filepath.Join(abs, "jobs"), 0o755); err != nil {
		return nil, model.NewError(model.StageReceive, "store-mkdir", abs, "", 0, err)
	}
	return &Store{root: abs}, nil
}

// checkName rejects absolute names, NUL bytes and parent traversal in a raw
// caller-supplied relative name, before any path joining can collapse "..".
func checkName(name string) error {
	if filepath.IsAbs(name) || strings.Contains(name, "\x00") {
		return fmt.Errorf("artifact path escapes work directory: %q", name)
	}
	for _, part := range strings.Split(filepath.ToSlash(name), "/") {
		if part == ".." {
			return fmt.Errorf("artifact path escapes work directory: %q", name)
		}
	}
	return nil
}

// checkWithin rejects absolute names, parent traversals and NUL bytes, then
// joins name under dir and confirms the cleaned result stays inside dir.
func checkWithin(dir, name string) error {
	if err := checkName(name); err != nil {
		return model.NewError(model.StageCommit, "path-guard", dir, "", 0, err)
	}
	base := filepath.Clean(dir)
	target := filepath.Clean(filepath.Join(base, name))
	if target != base && !strings.HasPrefix(target, base+string(os.PathSeparator)) {
		return model.NewError(model.StageCommit, "path-guard", dir, "", 0,
			fmt.Errorf("artifact path escapes work directory: %q", name))
	}
	return nil
}

func jobDirName(taskKey string) string {
	sum := sha256.Sum256([]byte(taskKey))
	return hex.EncodeToString(sum[:16])
}

func (s *Store) jobDir(taskKey string) string {
	return filepath.Join(s.root, "jobs", jobDirName(taskKey))
}

// Start opens a fresh job, or resumes an existing one when a checkpoint is
// present. inputHash identifies the exact fixture bytes: resuming against
// different inputs is refused rather than silently mixed.
func (s *Store) Start(taskKey, inputHash string) (*Job, *Recovery, error) {
	dir := s.jobDir(taskKey)
	if err := os.MkdirAll(filepath.Join(dir, "artifacts"), 0o755); err != nil {
		return nil, nil, model.NewError(model.StageReceive, "job-mkdir", dir, "", 0, err)
	}
	prev, err := readCheckpoint(dir)
	if err != nil {
		return nil, nil, err
	}
	if prev == nil {
		j := &Job{store: s, dir: dir, State: &State{
			TaskKey: taskKey, InputHash: inputHash,
			Stage: model.StageReceive, Attempts: 1,
		}}
		if err := j.persist(); err != nil {
			return nil, nil, err
		}
		return j, &Recovery{}, nil
	}
	if prev.InputHash != inputHash {
		return nil, nil, model.NewError(model.StageCommit, "input-mismatch", dir, "",
			0, fmt.Errorf("%w: checkpoint input %s vs current %s",
				model.ErrRecovery, prev.InputHash, inputHash))
	}
	rec := &Recovery{Recovered: true}
	j := &Job{store: s, dir: dir, State: prev}
	j.State.Attempts++
	// Preserve the prior failure so a restart never silently erases the
	// reason the previous attempt did not reach commit.
	prevErr := j.State.LastError
	if err := j.verifyArtifacts(&rec.VerifiedArtifacts); err != nil {
		return nil, nil, err
	}
	j.State.LastError = prevErr
	if err := j.persist(); err != nil {
		return nil, nil, err
	}
	return j, rec, nil
}

// verifyArtifacts confirms every written artifact still exists with its
// recorded hash. A missing or altered artifact means the prior run cannot be
// resumed without republishing, which the caller must treat as a failure.
func (j *Job) verifyArtifacts(verified *int) error {
	for i := range j.State.Artifacts {
		rec := &j.State.Artifacts[i]
		if !rec.Written {
			continue
		}
		path := filepath.Join(j.dir, rec.RelPath)
		raw, err := os.ReadFile(path)
		if err != nil {
			return model.NewError(model.StageArtifact, "verify-artifact", path,
				rec.Key, 0, fmt.Errorf("%w: %v", model.ErrRecovery, err))
		}
		sum := sha256.Sum256(raw)
		if got := hex.EncodeToString(sum[:]); got != rec.Sha256 ||
			int64(len(raw)) != rec.Size {
			return model.NewError(model.StageArtifact, "verify-hash", path,
				rec.Key, 0, fmt.Errorf("%w: recorded=%s actual=%s",
					model.ErrRecovery, rec.Sha256, got))
		}
		*verified++
	}
	return nil
}

// persist atomically writes the current state and bumps the revision.
func (j *Job) persist() error {
	j.State.Revision++
	if err := writeAtomic(j.dir, checkpointFile, j.State); err != nil {
		return err
	}
	return nil
}

// Advance moves the job to a new stage and checkpoints atomically. Stages
// only move forward; a backwards transition is a programming error.
func (j *Job) Advance(stage model.Stage) error {
	if stageRank(stage) < stageRank(j.State.Stage) {
		return model.NewError(stage, "stage-regress", j.dir, "", 0,
			fmt.Errorf("%w: %s -> %s", model.ErrCheckpoint, j.State.Stage, stage))
	}
	j.State.Stage = stage
	return j.persist()
}

// Reach advances the job to stage unless it has already reached or passed it.
// A resumed run uses this so re-executing an earlier stage's checkpoint is a
// no-op rather than a backwards transition.
func (j *Job) Reach(stage model.Stage) error {
	if stageRank(stage) <= stageRank(j.State.Stage) {
		return nil
	}
	return j.Advance(stage)
}

func stageRank(s model.Stage) int {
	switch s {
	case model.StageReceive:
		return 1
	case model.StageParse:
		return 2
	case model.StageArtifact:
		return 3
	case model.StageCommit:
		return 4
	}
	return 0
}

// Artifact returns the recorded artifact for a key, if any.
func (j *Job) Artifact(key string) (ArtifactRecord, bool) {
	for _, rec := range j.State.Artifacts {
		if rec.Key == key {
			return rec, true
		}
	}
	return ArtifactRecord{}, false
}

// IsCommitted reports whether key already has a committed manifest entry.
func (j *Job) IsCommitted(key string) bool {
	for _, k := range j.State.Published {
		if k == key {
			return true
		}
	}
	return false
}

// WriteArtifact writes content under the job's artifact directory, hashes it,
// records it and checkpoints at the artifact stage. An existing verified
// artifact is left untouched and the write is skipped, which is what makes a
// post-artifact restart idempotent.
func (j *Job) WriteArtifact(key, relName string, content []byte) (ArtifactRecord, error) {
	if rec, ok := j.Artifact(key); ok && rec.Written {
		return rec, nil
	}
	// Guard the caller-supplied name before filepath.Join could collapse a
	// ".." component out of sight.
	if err := checkName(relName); err != nil {
		return ArtifactRecord{}, model.NewError(model.StageCommit, "path-guard",
			j.dir, "", 0, err)
	}
	relName = filepath.Join("artifacts", relName)
	if err := checkWithin(j.dir, relName); err != nil {
		return ArtifactRecord{}, err
	}
	path := filepath.Join(j.dir, relName)
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return ArtifactRecord{}, model.NewError(model.StageArtifact, "mkdir",
			path, key, 0, err)
	}
	sum := sha256.Sum256(content)
	if err := os.WriteFile(path, content, 0o600); err != nil {
		return ArtifactRecord{}, model.NewError(model.StageArtifact, "write",
			path, key, 0, err)
	}
	rec := ArtifactRecord{
		Key: key, RelPath: relName,
		Sha256: hex.EncodeToString(sum[:]), Size: int64(len(content)), Written: true,
	}
	j.State.Artifacts = append(j.State.Artifacts, rec)
	if err := j.Advance(model.StageArtifact); err != nil {
		return ArtifactRecord{}, err
	}
	return rec, nil
}

// CommitKey marks an artifact committed and records its manifest entry key,
// then checkpoints at the commit stage. Already-committed keys are skipped so
// recovery never double-publishes.
func (j *Job) CommitKey(key string) error {
	if j.IsCommitted(key) {
		return nil
	}
	for i := range j.State.Artifacts {
		if j.State.Artifacts[i].Key == key {
			j.State.Artifacts[i].Committed = true
		}
	}
	j.State.Published = append(j.State.Published, key)
	return j.Advance(model.StageCommit)
}

// Fail records a locatable failure and checkpoints; it never rewrites the
// stage backwards and never reports success on failure.
func (j *Job) Fail(err error) error {
	me := model.AsError(err)
	if me == nil {
		me = model.NewError(j.State.Stage, "unknown", j.dir, "", 0, err)
	}
	j.State.LastError = me
	return j.persist()
}

// Dir exposes the confined job directory (used by the manifest writer).
func (j *Job) Dir() string { return j.dir }
