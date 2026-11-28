// Package stage persists resumable pipeline state.
//
// A run directory holds one state file plus one artifact per stage. Stage
// artifacts are written to a temporary name and only renamed into place after
// their checksum verifies, so an interrupted process can never leave a
// complete-looking but partial result. On restart, completed stages whose
// checksum still matches are reused; anything missing or corrupt is redone.
package stage

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"

	"rtptrace/internal/fault"
)

// Status is the lifecycle state of one stage.
type Status string

const (
	StatusPending Status = "pending"
	StatusRunning Status = "running"
	StatusDone    Status = "done"
	StatusFailed  Status = "failed"
)

// StageVersion is bumped whenever a stage's on-disk artifact format changes,
// invalidating old checkpoints automatically.
const StageVersion = 1

// Record is the persisted state of a single stage.
type Record struct {
	Name       string `json:"name"`
	Version    int    `json:"version"`
	Status     Status `json:"status"`
	Attempts   int    `json:"attempts"`
	Artifact   string `json:"artifact,omitempty"` // relative path under root
	SHA256     string `json:"sha256,omitempty"`
	Size       int64  `json:"size,omitempty"`
	StartedMS  int64  `json:"started_ms,omitempty"`
	UpdatedMS  int64  `json:"updated_ms,omitempty"`
	Error      string `json:"error,omitempty"`
}

// State is the full persisted run state, ordered deterministically by stage.
type State struct {
	InputDigest string             `json:"input_digest"`
	Seed        int64              `json:"seed"`
	Stages      map[string]*Record `json:"stages"`
}

// Store manages JSON state plus atomic artifacts inside a root directory.
type Store struct {
	root      string
	statePath string
	now       func() int64 // injectable clock in milliseconds
}

const stateFileName = "state.json"
const tmpSuffix = ".tmp"

// OpenStore creates root if needed and loads any existing state.
func OpenStore(root string, now func() int64) (*Store, error) {
	if now == nil {
		now = nowMS
	}
	if err := os.MkdirAll(root, 0o755); err != nil {
		return nil, fault.Newf(fault.KindInternal, "root", fault.NoOffset,
			"create root %s: %v", root, err)
	}
	s := &Store{root: root, statePath: filepath.Join(root, stateFileName), now: now}
	return s, nil
}

// Root returns the run directory.
func (s *Store) Root() string { return s.root }

// Load reads state; a missing file yields an empty (nil) state, not an error.
func (s *Store) Load() (*State, error) {
	data, err := os.ReadFile(s.statePath)
	if os.IsNotExist(err) {
		return nil, nil
	}
	if err != nil {
		return nil, fault.Newf(fault.KindInternal, "state.json", fault.NoOffset, "read: %v", err)
	}
	var st State
	if err := json.Unmarshal(data, &st); err != nil {
		return nil, fault.Newf(fault.KindCorrupt, "state.json", fault.NoOffset,
			"unparseable state: %v", err)
	}
	return &st, nil
}

// save writes state to a temp file and atomically renames it.
func (s *Store) save(st *State) error {
	data, err := json.MarshalIndent(st, "", "  ")
	if err != nil {
		return fault.Newf(fault.KindInternal, "state.json", fault.NoOffset, "marshal: %v", err)
	}
	data = append(data, '\n')
	tmp := s.statePath + tmpSuffix
	if err := os.WriteFile(tmp, data, 0o644); err != nil {
		return fault.Newf(fault.KindInternal, "state.json", fault.NoOffset, "write tmp: %v", err)
	}
	if err := os.Rename(tmp, s.statePath); err != nil {
		return fault.Newf(fault.KindInternal, "state.json", fault.NoOffset, "rename: %v", err)
	}
	return nil
}

// Ensure initializes records for names in order, preserving existing ones.
func (s *Store) Ensure(inputDigest string, seed int64, names []string) (*State, error) {
	st, err := s.Load()
	if err != nil {
		return nil, err
	}
	if st == nil {
		st = &State{InputDigest: inputDigest, Seed: seed, Stages: map[string]*Record{}}
	}
	for _, name := range names {
		if _, ok := st.Stages[name]; !ok {
			st.Stages[name] = &Record{Name: name, Version: StageVersion, Status: StatusPending}
		}
	}
	if err := s.save(st); err != nil {
		return nil, err
	}
	return st, nil
}

// MarkRunning transitions a stage to running and persists.
func (s *Store) MarkRunning(st *State, name string) error {
	rec := mustRecord(st, name)
	rec.Status = StatusRunning
	rec.Attempts++
	rec.StartedMS = s.now()
	rec.UpdatedMS = rec.StartedMS
	rec.Error = ""
	return s.save(st)
}

// MarkFailed records a failure and persists.
func (s *Store) MarkFailed(st *State, name string, cause error) error {
	rec := mustRecord(st, name)
	rec.Status = StatusFailed
	rec.UpdatedMS = s.now()
	if cause != nil {
		rec.Error = cause.Error()
	}
	return s.save(st)
}

// CommitArtifact verifies data's checksum, writes it atomically and marks the
// stage done. The artifact name is fixed per stage so reruns overwrite the
// same path deterministically.
func (s *Store) CommitArtifact(st *State, name string, relPath string, data []byte) (string, error) {
	sum := sha256.Sum256(data)
	digest := hex.EncodeToString(sum[:])

	abs := filepath.Join(s.root, relPath)
	if err := os.MkdirAll(filepath.Dir(abs), 0o755); err != nil {
		return "", fault.Newf(fault.KindInternal, relPath, fault.NoOffset, "mkdir: %v", err)
	}
	tmp := abs + tmpSuffix
	if err := os.WriteFile(tmp, data, 0o644); err != nil {
		return "", fault.Newf(fault.KindInternal, relPath, fault.NoOffset, "write tmp: %v", err)
	}
	// Verify the bytes on disk before promoting, catching a torn write.
	got, err := fileSHA256(tmp)
	if err != nil {
		return "", err
	}
	if got != digest {
		_ = os.Remove(tmp)
		return "", fault.Newf(fault.KindInternal, relPath, fault.NoOffset,
			"checksum mismatch before commit: %s != %s", got, digest)
	}
	if err := os.Rename(tmp, abs); err != nil {
		return "", fault.Newf(fault.KindInternal, relPath, fault.NoOffset, "rename: %v", err)
	}

	rec := mustRecord(st, name)
	rec.Status = StatusDone
	rec.Artifact = relPath
	rec.SHA256 = digest
	rec.Size = int64(len(data))
	rec.UpdatedMS = s.now()
	rec.Error = ""
	if err := s.save(st); err != nil {
		return "", err
	}
	return digest, nil
}

// VerifyDone returns nil when the stage is done and its on-disk artifact still
// matches the recorded checksum; otherwise the stage must be re-executed.
func (s *Store) VerifyDone(rec *Record) error {
	if rec == nil || rec.Status != StatusDone || rec.Artifact == "" {
		return fault.New(fault.KindInternal, rec.Name, fault.NoOffset, "stage not done")
	}
	if rec.Version != StageVersion {
		return fault.Newf(fault.KindCorrupt, rec.Name, fault.NoOffset,
			"stage version %d != current %d", rec.Version, StageVersion)
	}
	abs := filepath.Join(s.root, rec.Artifact)
	got, err := fileSHA256(abs)
	if err != nil {
		return fault.Newf(fault.KindCorrupt, rec.Name, fault.NoOffset, "artifact unreadable: %v", err)
	}
	if got != rec.SHA256 {
		return fault.Newf(fault.KindCorrupt, rec.Name, fault.NoOffset,
			"artifact checksum drift: %s != %s", got, rec.SHA256)
	}
	return nil
}

// ReadArtifact returns the verified bytes of a done stage.
func (s *Store) ReadArtifact(rec *Record) ([]byte, error) {
	if err := s.VerifyDone(rec); err != nil {
		return nil, err
	}
	return os.ReadFile(filepath.Join(s.root, rec.Artifact))
}

// OrderedRecords returns records in a stable order (by stage name).
func OrderedRecords(st *State) []*Record {
	names := make([]string, 0, len(st.Stages))
	for n := range st.Stages {
		names = append(names, n)
	}
	sort.Strings(names)
	out := make([]*Record, 0, len(names))
	for _, n := range names {
		out = append(out, st.Stages[n])
	}
	return out
}

func mustRecord(st *State, name string) *Record {
	rec, ok := st.Stages[name]
	if !ok {
		// This is a programming error: Ensure must run first.
		panic(fmt.Sprintf("stage %q not initialized", name))
	}
	return rec
}

func fileSHA256(path string) (string, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return "", fault.Newf(fault.KindCorrupt, path, fault.NoOffset, "read: %v", err)
	}
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:]), nil
}
