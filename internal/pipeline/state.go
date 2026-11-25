package pipeline

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
	"sync"

	"mediacore/internal/clock"
)

// ErrSimulatedCrash is returned after a product is durable but before state commit.
var ErrSimulatedCrash = errors.New("simulated process exit after publish")

// ArtifactState records one durable stage output.
type ArtifactState struct {
	Stage    string `json:"stage"`
	Path     string `json:"path"`
	SHA256   string `json:"sha256"`
	Verified bool   `json:"verified"`
}

// State is the resumable pipeline state.
type State struct {
	BatchID   string                   `json:"batch_id"`
	Completed []string                 `json:"completed"`
	Artifacts map[string]ArtifactState `json:"artifacts"`
	Published bool                     `json:"published"`
}

// Store persists state atomically and records recovery decisions.
type Store struct {
	path string
	mu   sync.Mutex
}

func NewStore(path string) *Store { return &Store{path: path} }

func (s *Store) Load() (*State, error) {
	data, err := os.ReadFile(s.path)
	if errors.Is(err, os.ErrNotExist) {
		return &State{Artifacts: map[string]ArtifactState{}}, nil
	}
	if err != nil {
		return nil, clock.Wrap(clock.CategoryState, s.path, 0, 0, "read state", err)
	}
	var st State
	if err := json.Unmarshal(data, &st); err != nil {
		return nil, clock.Wrap(clock.CategoryState, s.path, 0, 0, "decode state", err)
	}
	if st.Artifacts == nil {
		st.Artifacts = map[string]ArtifactState{}
	}
	return &st, nil
}

func (s *Store) Save(st *State) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	data, err := json.MarshalIndent(st, "", "  ")
	if err != nil {
		return clock.Wrap(clock.CategoryState, s.path, 0, 0, "encode state", err)
	}
	if _, err := WriteAtomicVerified(s.path, data); err != nil {
		return clock.Wrap(clock.CategoryState, s.path, 0, 0, "save state", err)
	}
	return nil
}

func (st *State) complete(stage string, art ArtifactState) {
	st.Artifacts[stage] = art
	for _, x := range st.Completed {
		if x == stage {
			return
		}
	}
	st.Completed = append(st.Completed, stage)
}

// WriteAtomicVerified writes a temp file, re-reads and hashes it, then renames.
func WriteAtomicVerified(path string, data []byte) (string, error) {
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return "", err
	}
	tmp, err := os.CreateTemp(dir, ".mediacore-tmp-*")
	if err != nil {
		return "", err
	}
	tmpName := tmp.Name()
	defer os.Remove(tmpName)
	h := sha256.New()
	mw := io.MultiWriter(tmp, h)
	if _, err := mw.Write(data); err != nil {
		tmp.Close()
		return "", err
	}
	if err := tmp.Sync(); err != nil {
		tmp.Close()
		return "", err
	}
	if err := tmp.Close(); err != nil {
		return "", err
	}
	got, err := HashFile(tmpName)
	if err != nil {
		return "", err
	}
	want := hex.EncodeToString(h.Sum(nil))
	if got != want {
		return "", clock.New(clock.CategoryHashMismatch, path, 0, 0, "temp artifact hash verification failed")
	}
	if err := os.Rename(tmpName, path); err != nil {
		return "", err
	}
	return want, nil
}

// HashFile returns the SHA-256 content hash of path.
func HashFile(path string) (string, error) {
	f, err := os.Open(path)
	if err != nil {
		return "", err
	}
	defer f.Close()
	h := sha256.New()
	if _, err := io.Copy(h, f); err != nil {
		return "", err
	}
	return hex.EncodeToString(h.Sum(nil)), nil
}

func fileHashMatches(path, want string) (bool, error) {
	got, err := HashFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	return got == want, nil
}
