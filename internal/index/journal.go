// Package index implements the durable, resumable local journal. State is
// committed through an atomic temp-file rename; artifacts are content
// addressed by SHA-256 so a re-run registers identical output as completed
// instead of republishing, while a hash collision (same path, different
// content) is a hard failure.
package index

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sync"

	"continuity/internal/clock"
)

// Stage enumerates the pipeline phases in execution order.
type Stage string

const (
	StageParse    Stage = "parse"
	StageAlign    Stage = "align"
	StageVerify   Stage = "verify"
	StageWrite    Stage = "write"
	StagePublish  Stage = "publish"
)

// OrderedStages is the canonical phase order.
var OrderedStages = []Stage{StageParse, StageAlign, StageVerify, StageWrite, StagePublish}

// Artifact records one content-addressed product written to the output store.
type Artifact struct {
	StorePath  string `json:"store_path"` // content-derived relative path
	SHA256     string `json:"sha256"`
	Size       int64  `json:"size"`
	RepID      string `json:"representation"`
	SegmentIdx int    `json:"segment_idx"`
	Published  bool   `json:"published"`
}

// Checkpoint is the persisted journal document.
type Checkpoint struct {
	Version          int                 `json:"version"`
	InputRoot        string              `json:"input_root"`
	CurrentStage     Stage               `json:"current_stage"`
	StageCompleted   map[Stage]bool      `json:"stage_completed"`
	NextChunk        int                 `json:"next_chunk"`     // chunks fully processed
	SegmentsDone     int                 `json:"segments_done"`  // segment count reflected in journal
	Artifacts        map[string]*Artifact `json:"artifacts"`      // key = StorePath
	Recovered        int                 `json:"recovered"`      // gaps filled from backup
	DuplicatePublish int                 `json:"duplicate_publish"`
	ManifestSHA      string              `json:"manifest_sha"`   // set only on publish
	Published        bool                `json:"published"`
}

// Journal is the file-backed, mutex-guarded checkpoint store.
type Journal struct {
	mu       sync.Mutex
	path     string
	state    Checkpoint
	crashCfg CrashConfig
}

// CrashConfig controls write-then-commit fault injection.
type CrashConfig struct {
	// CrashAfterWrite > 0 means: simulate an exit after the Nth artifact
	// content write but before its state commit.
	CrashAfterWrite int
	writesObserved  int
}

// InjectedCrash is the sentinel error for fault injection (exit code 75).
type InjectedCrash struct {
	Detail string
}

func (e *InjectedCrash) Error() string { return "injected crash: " + e.Detail }

// NewJournal opens an existing journal or initializes one at dir/journal.json.
func NewJournal(dir, inputRoot string) (*Journal, error) {
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return nil, clock.New(clock.KindIO, "journal", "mkdir: %v", err)
	}
	j := &Journal{
		path: filepath.Join(dir, "journal.json"),
		state: Checkpoint{
			Version:        1,
			InputRoot:      inputRoot,
			StageCompleted: map[Stage]bool{},
			Artifacts:      map[string]*Artifact{},
		},
	}
	if raw, err := os.ReadFile(j.path); err == nil {
		if err := json.Unmarshal(raw, &j.state); err != nil {
			return nil, clock.New(clock.KindCorruptMedia, "journal",
				"corrupt journal %s: %v", j.path, err)
		}
		if j.state.Artifacts == nil {
			j.state.Artifacts = map[string]*Artifact{}
		}
		if j.state.StageCompleted == nil {
			j.state.StageCompleted = map[Stage]bool{}
		}
	} else if !os.IsNotExist(err) {
		return nil, clock.New(clock.KindIO, "journal", "read: %v", err)
	}
	return j, nil
}

// SetCrashConfig attaches fault-injection settings.
func (j *Journal) SetCrashConfig(c CrashConfig) {
	j.mu.Lock()
	defer j.mu.Unlock()
	j.crashCfg = c
}

// State returns a copy of the checkpoint for read-only inspection.
func (j *Journal) State() Checkpoint {
	j.mu.Lock()
	defer j.mu.Unlock()
	cp := j.state
	cp.Artifacts = make(map[string]*Artifact, len(j.state.Artifacts))
	for k, v := range j.state.Artifacts {
		vc := *v
		cp.Artifacts[k] = &vc
	}
	cp.StageCompleted = make(map[Stage]bool, len(j.state.StageCompleted))
	for k, v := range j.state.StageCompleted {
		cp.StageCompleted[k] = v
	}
	return cp
}

// StorePath returns the deterministic content-addressed relative path.
func StorePath(sha string, repID string, idx int) string {
	return filepath.Join(repID, fmt.Sprintf("seg-%06d-%s.m4s", idx, sha[:16]))
}

// HashBytes returns the hex SHA-256 of data.
func HashBytes(data []byte) string {
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:])
}

// RegisterArtifact implements the write-then-commit protocol.
//
//	1. The caller has already written content to the store at StorePath.
//	2. If the path exists with the same hash -> completed, no republish.
//	3. If the path exists with a different hash -> hash conflict error.
//	4. Otherwise commit the new artifact, then optionally inject a crash
//	   BEFORE the durable checkpoint rename (state never commits).
func (j *Journal) RegisterArtifact(a Artifact) error {
	j.mu.Lock()
	defer j.mu.Unlock()
	if existing, ok := j.state.Artifacts[a.StorePath]; ok {
		if existing.SHA256 == a.SHA256 {
			// Idempotent re-run: already recorded, nothing republished.
			return nil
		}
		return clock.New(clock.KindHashConflict, "journal",
			"hash conflict at %s: existing %s vs new %s",
			a.StorePath, existing.SHA256, a.SHA256)
	}
	cp := a
	j.state.Artifacts[a.StorePath] = &cp
	j.state.SegmentsDone++

	// Fault injection: content is on disk, but the checkpoint rename below
	// is skipped and the in-memory entry is dropped, so resume must recover.
	if j.crashCfg.CrashAfterWrite > 0 {
		j.crashCfg.writesObserved++
		if j.crashCfg.writesObserved == j.crashCfg.CrashAfterWrite {
			delete(j.state.Artifacts, a.StorePath)
			j.state.SegmentsDone--
			return &InjectedCrash{Detail: fmt.Sprintf(
				"after writing %s, before state commit", a.StorePath)}
		}
	}
	return j.persistLocked()
}

// NoteRecovered increments the backup-gap recovery counter and persists.
func (j *Journal) NoteRecovered(n int) error {
	j.mu.Lock()
	defer j.mu.Unlock()
	j.state.Recovered += n
	return j.persistLocked()
}

// NoteDuplicatePublish increments the idempotent re-run counter.
func (j *Journal) NoteDuplicatePublish(n int) error {
	j.mu.Lock()
	defer j.mu.Unlock()
	j.state.DuplicatePublish += n
	return j.persistLocked()
}

// AdvanceStage marks stage complete and moves to the next stage, persisting.
func (j *Journal) AdvanceStage(stage Stage) error {
	j.mu.Lock()
	defer j.mu.Unlock()
	j.state.StageCompleted[stage] = true
	j.state.CurrentStage = stage
	return j.persistLocked()
}

// CheckpointChunk records that another chunk (every `chunk` segments) has
// been fully processed, persisting the journal.
func (j *Journal) CheckpointChunk(chunkNo, segmentsInChunk int) error {
	j.mu.Lock()
	defer j.mu.Unlock()
	j.state.NextChunk = chunkNo
	return j.persistLocked()
}

// MarkPublished records the final manifest hash atomically with publish.
func (j *Journal) MarkPublished(manifestSHA string) error {
	j.mu.Lock()
	defer j.mu.Unlock()
	j.state.Published = true
	j.state.ManifestSHA = manifestSHA
	j.state.StageCompleted[StagePublish] = true
	j.state.CurrentStage = StagePublish
	return j.persistLocked()
}

// IsStageDone reports whether a stage has a committed completion marker.
func (j *Journal) IsStageDone(stage Stage) bool {
	j.mu.Lock()
	defer j.mu.Unlock()
	return j.state.StageCompleted[stage]
}

// persistLocked writes the journal via temp file + fsync + atomic rename.
func (j *Journal) persistLocked() error {
	raw, err := json.MarshalIndent(&j.state, "", "  ")
	if err != nil {
		return clock.New(clock.KindIO, "journal", "marshal: %v", err)
	}
	dir := filepath.Dir(j.path)
	tmp := filepath.Join(dir, ".journal.tmp")
	if err := os.WriteFile(tmp, raw, 0o644); err != nil {
		return clock.New(clock.KindIO, "journal", "write temp: %v", err)
	}
	f, err := os.Open(tmp)
	if err != nil {
		return clock.New(clock.KindIO, "journal", "fsync open: %v", err)
	}
	if err := f.Sync(); err != nil {
		f.Close()
		return clock.New(clock.KindIO, "journal", "fsync: %v", err)
	}
	if err := f.Close(); err != nil {
		return clock.New(clock.KindIO, "journal", "close: %v", err)
	}
	if err := os.Rename(tmp, j.path); err != nil {
		return clock.New(clock.KindIO, "journal", "rename: %v", err)
	}
	return nil
}

// ArtifactByHash looks up a committed artifact by content hash (used by
// resume to reconcile an orphaned content file).
func (j *Journal) ArtifactByHash(sha string) (*Artifact, bool) {
	j.mu.Lock()
	defer j.mu.Unlock()
	for _, a := range j.state.Artifacts {
		if a.SHA256 == sha {
			cp := *a
			return &cp, true
		}
	}
	return nil, false
}
