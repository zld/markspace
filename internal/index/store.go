// Package index writes deterministic chunk records and atomic checkpoints.
package index

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"

	"mediapack/internal/fault"
	"mediapack/internal/pathsafe"
)

const checkpointVersion = 1

type ChunkRecord struct {
	Sequence         int      `json:"sequence"`
	ChunkID          string   `json:"chunk_id"`
	TrackID          uint32   `json:"track_id"`
	ChunkIndex       int      `json:"chunk_index"`
	FirstSampleIndex int      `json:"first_sample_index"`
	SampleCount      int      `json:"sample_count"`
	PayloadBytes     int64    `json:"payload_bytes"`
	PayloadHashes    []string `json:"payload_hashes"`
	InputPath        string   `json:"input_path,omitempty"`
	InputEndOffset   int64    `json:"input_end_offset,omitempty"`
}

type Checkpoint struct {
	Version           int   `json:"version"`
	LastSequence      int   `json:"last_sequence"`
	CommittedSequence []int `json:"committed_sequences"`
	TotalSamples      int   `json:"total_samples"`
	TotalPayloadBytes int64 `json:"total_payload_bytes"`
	RecoveredChunks   int   `json:"recovered_chunks_total"`
}

type Store struct {
	root          string
	chunkDir      string
	checkDir      string
	mu            sync.Mutex
	checkpoint    *Checkpoint
	hadCheckpoint bool
}

func Open(root string) (*Store, error) {
	chunkDir, err := pathsafe.Join(root, "chunks")
	if err != nil {
		return nil, err
	}
	checkDir, err := pathsafe.Join(root, "checkpoints")
	if err != nil {
		return nil, err
	}
	for _, d := range []string{chunkDir, checkDir} {
		if err := os.MkdirAll(d, 0o755); err != nil {
			return nil, fault.Wrap(fault.KindIndex, err)
		}
	}
	s := &Store{root: root, chunkDir: chunkDir, checkDir: checkDir}
	cp, err := s.LoadCheckpoint()
	if err != nil {
		return nil, err
	}
	s.checkpoint = cp
	s.hadCheckpoint = cp.LastSequence > 0
	return s, nil
}

func (s *Store) HadCheckpoint() bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.hadCheckpoint
}

func (s *Store) AddRecoveredChunks(n int) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.checkpoint.RecoveredChunks += n
}

func (s *Store) Checkpoint() *Checkpoint {
	s.mu.Lock()
	defer s.mu.Unlock()
	cp := *s.checkpoint
	return &cp
}

// CommitChunk writes and verifies a temporary chunk record, atomically renames
// it, then advances the checkpoint. Replaying a sequence represented by the
// loaded checkpoint never adds its counts twice.
func (s *Store) CommitChunk(rec ChunkRecord) (*Checkpoint, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	rec.Sequence = s.checkpoint.LastSequence + 1
	if rec.PayloadHashes == nil {
		rec.PayloadHashes = []string{}
	}
	tmp, final, err := s.chunkPaths(rec)
	if err != nil {
		return nil, err
	}
	if err := writeAndVerifyJSON(tmp, rec); err != nil {
		return nil, err
	}
	var readBack ChunkRecord
	if err := readJSON(tmp, &readBack); err != nil {
		return nil, fault.Wrap(fault.KindIndex, err)
	}
	if readBack.SampleCount != rec.SampleCount || readBack.PayloadBytes != rec.PayloadBytes {
		return nil, fault.New(fault.KindIndex, "temporary chunk record verification failed: %s", tmp)
	}
	if err := os.Rename(tmp, final); err != nil {
		return nil, fault.Wrap(fault.KindIndex, err)
	}

	cp := *s.checkpoint
	cp.Version = checkpointVersion
	cp.LastSequence = rec.Sequence
	cp.CommittedSequence = append(append([]int(nil), cp.CommittedSequence...), rec.Sequence)
	cp.TotalSamples += rec.SampleCount
	cp.TotalPayloadBytes += rec.PayloadBytes
	if err := s.persistCheckpoint(&cp); err != nil {
		return nil, err
	}
	s.checkpoint = &cp
	out := cp
	return &out, nil
}

func (s *Store) IsCommittedSequence(seq int) bool {
	cp := s.Checkpoint()
	for _, n := range cp.CommittedSequence {
		if n == seq {
			return true
		}
	}
	return false
}

// LoadCheckpoint reads checkpoint.json when valid. If the pointer or its
// immutable checkpoint was deleted/corrupted, it falls back to the newest
// valid file in checkpoints/.
func (s *Store) LoadCheckpoint() (*Checkpoint, error) {
	pointer, err := pathsafe.Join(s.root, "checkpoint.json")
	if err != nil {
		return nil, err
	}
	var cp Checkpoint
	if err := readJSON(pointer, &cp); err == nil && cp.Version == checkpointVersion {
		immutable, _ := s.checkpointPath(cp.LastSequence)
		if _, statErr := os.Stat(immutable); statErr == nil {
			return &cp, nil
		}
	}
	return s.latestValidCheckpoint()
}

func (s *Store) latestValidCheckpoint() (*Checkpoint, error) {
	entries, err := os.ReadDir(s.checkDir)
	if err != nil {
		return nil, fault.Wrap(fault.KindIndex, err)
	}
	var valid []Checkpoint
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".json") {
			continue
		}
		p := filepath.Join(s.checkDir, e.Name())
		var cp Checkpoint
		if err := readJSON(p, &cp); err != nil || cp.Version != checkpointVersion {
			continue
		}
		valid = append(valid, cp)
	}
	if len(valid) == 0 {
		return &Checkpoint{Version: checkpointVersion, CommittedSequence: []int{}}, nil
	}
	sort.Slice(valid, func(i, j int) bool { return valid[i].LastSequence < valid[j].LastSequence })
	cp := valid[len(valid)-1]
	if cp.CommittedSequence == nil {
		cp.CommittedSequence = []int{}
	}
	// Repair the atomic pointer after fallback.
	_ = writeJSONAtomic(filepath.Join(s.root, "checkpoint.json"), cp)
	return &cp, nil
}

func (s *Store) persistCheckpoint(cp *Checkpoint) error {
	path, err := s.checkpointPath(cp.LastSequence)
	if err != nil {
		return err
	}
	if err := writeJSONAtomic(path, cp); err != nil {
		return err
	}
	pointer, err := pathsafe.Join(s.root, "checkpoint.json")
	if err != nil {
		return err
	}
	return writeJSONAtomic(pointer, cp)
}

func (s *Store) checkpointPath(seq int) (string, error) {
	return pathsafe.Join(s.root, "checkpoints", fmt.Sprintf("%06d.json", seq))
}

func (s *Store) chunkPaths(rec ChunkRecord) (tmp, final string, err error) {
	name := fmt.Sprintf("%06d-%s-t%d-c%d", rec.Sequence, safeID(rec.ChunkID), rec.TrackID, rec.ChunkIndex)
	tmp, err = pathsafe.Join(s.root, "chunks", name+".tmp")
	if err != nil {
		return "", "", err
	}
	final, err = pathsafe.Join(s.root, "chunks", name+".json")
	return tmp, final, err
}

func safeID(id string) string {
	id = strings.ReplaceAll(id, string(filepath.Separator), "_")
	id = strings.ReplaceAll(id, "..", "_")
	if id == "" {
		return "chunk"
	}
	return id
}

func writeAndVerifyJSON(path string, v any) error {
	if err := writeJSONAtomic(path+".writing", v); err != nil {
		return err
	}
	if err := os.Rename(path+".writing", path); err != nil {
		return fault.Wrap(fault.KindIndex, err)
	}
	var back map[string]any
	if err := readJSON(path, &back); err != nil {
		return fault.Wrap(fault.KindIndex, err)
	}
	return nil
}

func writeJSONAtomic(path string, v any) error {
	b, err := json.MarshalIndent(v, "", "  ")
	if err != nil {
		return fault.Wrap(fault.KindIndex, err)
	}
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, b, 0o644); err != nil {
		return fault.Wrap(fault.KindIndex, err)
	}
	if err := os.Rename(tmp, path); err != nil {
		return fault.Wrap(fault.KindIndex, err)
	}
	return nil
}

func readJSON(path string, v any) error {
	b, err := os.ReadFile(path)
	if err != nil {
		return err
	}
	if len(strings.TrimSpace(string(b))) == 0 {
		return errors.New("empty JSON record")
	}
	return json.Unmarshal(b, v)
}

func SaveJSON(root, name string, v any) (string, error) {
	p, err := pathsafe.Join(root, name)
	if err != nil {
		return "", err
	}
	if err := writeJSONAtomic(p, v); err != nil {
		return "", err
	}
	return p, nil
}

func LoadJSON(path string, v any) error {
	if err := readJSON(path, v); err != nil {
		return fault.Wrap(fault.KindIndex, err)
	}
	return nil
}
