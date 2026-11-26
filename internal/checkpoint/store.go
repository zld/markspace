// Package checkpoint persists node state and output hashes so an
// interrupted run can resume. Outputs are written to a temporary file and
// atomically renamed; on resume, recorded SHA-256 values are verified
// before a node is reused, and a mismatch forces recomputation.
package checkpoint

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"

	"pwf/internal/dag"
	"pwf/internal/media"
)

// NodeState is one checkpoint record.
type NodeState struct {
	Key          string `json:"key"`
	Status       string `json:"status"`
	SHA256       string `json:"sha256"`
	OutputFile   string `json:"output_file"`
	InputDigest  string `json:"input_digest"`
	ConfigDigest string `json:"config_digest"`
	Version      string `json:"version"`
}

// Store is a directory-backed checkpoint implementing dag.Committer. All
// mutations are serialized so concurrent workers never share a temp file.
type Store struct {
	dir    string
	index  string
	mu     sync.Mutex
	states map[string]NodeState
}

// Open creates or loads a checkpoint directory, rejecting path escapes.
func Open(workDir string) (*Store, error) {
	if err := safePath(workDir); err != nil {
		return nil, err
	}
	dir := filepath.Join(workDir, "checkpoint")
	if err := os.MkdirAll(filepath.Join(dir, "outputs"), 0o755); err != nil {
		return nil, media.NewError(media.KindCheckpoint, media.StageCheckpoint,
			"cannot create checkpoint dir", err)
	}
	s := &Store{dir: dir, index: filepath.Join(dir, "index.json"), states: map[string]NodeState{}}
	if err := s.load(); err != nil {
		return nil, err
	}
	return s, nil
}

func (s *Store) load() error {
	raw, err := os.ReadFile(s.index)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return media.NewError(media.KindCheckpoint, media.StageCheckpoint,
			"cannot read checkpoint index", err)
	}
	var states []NodeState
	if err := json.Unmarshal(raw, &states); err != nil {
		// Corrupt index: discard it; every node will be recomputed.
		s.states = map[string]NodeState{}
		return nil
	}
	for _, st := range states {
		s.states[st.Key] = st
	}
	return nil
}

// persist writes the index atomically alongside the outputs.
func (s *Store) persist() error {
	states := make([]NodeState, 0, len(s.states))
	for _, st := range s.states {
		states = append(states, st)
	}
	// Stable order by key for byte-identical resumes.
	sortStates(states)
	data, err := json.MarshalIndent(states, "", "  ")
	if err != nil {
		return err
	}
	tmp := s.index + ".tmp"
	if err := os.WriteFile(tmp, data, 0o644); err != nil {
		return err
	}
	return os.Rename(tmp, s.index)
}

// Commit implements dag.Committer: temp write, SHA-256, atomic rename. The
// whole operation is serialized so concurrent workers never race on the
// shared index temp file.
func (s *Store) Commit(nodeKey string, data []byte) (string, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := safeName(nodeKey); err != nil {
		return "", err
	}
	// Per-node unique temp name avoids any cross-node temp collision.
	final := filepath.Join(s.dir, "outputs", nodeKey+".out")
	tmp := final + ".tmp"
	sum := sha256.Sum256(data)
	sha := hex.EncodeToString(sum[:])
	if err := os.WriteFile(tmp, data, 0o644); err != nil {
		return "", media.NewError(media.KindCheckpoint, media.StageCheckpoint,
			"temp output write failed", err)
	}
	if err := os.Rename(tmp, final); err != nil {
		return "", media.NewError(media.KindCheckpoint, media.StageCheckpoint,
			"atomic rename failed", err)
	}
	st := NodeState{
		Key: nodeKey, Status: string(dag.StatusSucceeded), SHA256: sha,
		OutputFile: filepath.Base(final), Version: dag.Version,
	}
	if prev, ok := s.states[nodeKey]; ok {
		st.InputDigest, st.ConfigDigest = prev.InputDigest, prev.ConfigDigest
	}
	s.states[nodeKey] = st
	if err := s.persist(); err != nil {
		return "", err
	}
	return sha, nil
}

// Read implements dag.Committer.
func (s *Store) Read(nodeKey string) ([]byte, error) {
	if err := safeName(nodeKey); err != nil {
		return nil, err
	}
	return os.ReadFile(filepath.Join(s.dir, "outputs", nodeKey+".out"))
}

// RecordDigests stores the input/config digests that define a node key.
func (s *Store) RecordDigests(nodeKey, inputDigest, configDigest string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	st := s.states[nodeKey]
	st.Key, st.InputDigest, st.ConfigDigest, st.Version = nodeKey, inputDigest, configDigest, dag.Version
	s.states[nodeKey] = st
}

// Reusable is the executor Skip callback: a node is reused only when the
// checkpoint marks it succeeded, its recorded digest matches the output on
// disk, and version/digests match the current request. Anything damaged
// or stale returns false so the node is recomputed.
func (s *Store) Reusable(nodeKey, inputDigest, configDigest string) (string, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	st, ok := s.states[nodeKey]
	if !ok || st.Status != string(dag.StatusSucceeded) || st.Version != dag.Version {
		return "", false
	}
	if st.InputDigest != inputDigest || st.ConfigDigest != configDigest {
		return "", false
	}
	data, err := os.ReadFile(filepath.Join(s.dir, "outputs", st.OutputFile))
	if err != nil {
		return "", false
	}
	sum := sha256.Sum256(data)
	if hex.EncodeToString(sum[:]) != st.SHA256 {
		// Tampered output: drop the stale record and recompute.
		delete(s.states, nodeKey)
		_ = s.persist()
		return "", false
	}
	return st.SHA256, true
}

// States returns a snapshot of all checkpoint states.
func (s *Store) States() []NodeState {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make([]NodeState, 0, len(s.states))
	for _, st := range s.states {
		out = append(out, st)
	}
	sortStates(out)
	return out
}

// safePath rejects absolute traversal and ".." components.
func safePath(p string) error {
	if filepath.IsAbs(p) {
		// Absolute work dirs are allowed by the CLI, but never a traversal.
	}
	cleaned := filepath.Clean(p)
	if strings.Contains(cleaned, "..") {
		return media.NewError(media.KindPathEscape, media.StageCheckpoint,
			fmt.Sprintf("path traversal rejected: %s", p), nil)
	}
	return nil
}

// safeName ensures node keys cannot escape the outputs directory.
func safeName(name string) error {
	if name == "" || strings.ContainsAny(name, `/\`) || name == "." || name == ".." {
		return media.NewError(media.KindPathEscape, media.StageCheckpoint,
			fmt.Sprintf("unsafe node key: %q", name), nil)
	}
	return nil
}

func sortStates(states []NodeState) {
	for i := 1; i < len(states); i++ {
		for j := i; j > 0 && states[j-1].Key > states[j].Key; j-- {
			states[j-1], states[j] = states[j], states[j-1]
		}
	}
}
