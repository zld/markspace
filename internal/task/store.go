// Package task persists per-task pipeline state to the local filesystem.
// Each completed stage writes a checkpoint; restarts resume from the last
// stage whose checkpoint verifies, and a failed verification rolls back
// and redoes that stage. Unfinished results are never marked successful.
package task

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"os"
	"path/filepath"
	"regexp"

	"mediacore/internal/model"
)

// StageOrder is the fixed pipeline phase sequence.
var StageOrder = []model.Stage{
	model.StageDemux, model.StageMap, model.StageRemux, model.StageManifest,
}

// StageIndex returns the position of a stage in StageOrder, or -1.
func StageIndex(s model.Stage) int {
	for i, v := range StageOrder {
		if v == s {
			return i
		}
	}
	return -1
}

// Checkpoint records one completed stage and the digest of its payload.
type Checkpoint struct {
	TaskKey string      `json:"task_key"`
	Stage   model.Stage `json:"stage"`
	Digest  string      `json:"digest"` // sha256 hex of the stage payload
	Size    int64       `json:"size"`
	Done    bool        `json:"done"`
}

var validKey = regexp.MustCompile(`^[A-Za-z0-9._-]+$`)

// Store keeps task state under Dir, one subdirectory per task key.
type Store struct {
	Dir string
}

func NewStore(dir string) *Store { return &Store{Dir: dir} }

func (s *Store) taskDir(taskKey string) (string, error) {
	if !validKey.MatchString(taskKey) {
		return "", model.NewError(model.ErrIO, model.StageManifest, "", -1,
			"unsafe task key %q", taskKey)
	}
	dir := filepath.Join(s.Dir, taskKey)
	clean := filepath.Clean(dir)
	if filepath.Dir(clean) != filepath.Clean(s.Dir) {
		return "", model.NewError(model.ErrIO, model.StageManifest, "", -1,
			"task key escapes store dir: %q", taskKey)
	}
	return clean, nil
}

// Save atomically writes a stage payload and its checkpoint.
func (s *Store) Save(taskKey string, stage model.Stage, payload []byte) error {
	dir, err := s.taskDir(taskKey)
	if err != nil {
		return err
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return model.NewError(model.ErrIO, stage, "", -1, "mkdir: %v", err)
	}
	sum := sha256.Sum256(payload)
	cp := Checkpoint{
		TaskKey: taskKey, Stage: stage,
		Digest: hex.EncodeToString(sum[:]),
		Size:   int64(len(payload)), Done: true,
	}
	binTmp := filepath.Join(dir, string(stage)+".bin.tmp")
	if err := os.WriteFile(binTmp, payload, 0o644); err != nil {
		return model.NewError(model.ErrIO, stage, "", -1, "write payload: %v", err)
	}
	if err := os.Rename(binTmp, filepath.Join(dir, string(stage)+".bin")); err != nil {
		return model.NewError(model.ErrIO, stage, "", -1, "rename payload: %v", err)
	}
	meta, _ := json.Marshal(cp)
	jsonTmp := filepath.Join(dir, string(stage)+".json.tmp")
	if err := os.WriteFile(jsonTmp, meta, 0o644); err != nil {
		return model.NewError(model.ErrIO, stage, "", -1, "write checkpoint: %v", err)
	}
	if err := os.Rename(jsonTmp, filepath.Join(dir, string(stage)+".json")); err != nil {
		return model.NewError(model.ErrIO, stage, "", -1, "rename checkpoint: %v", err)
	}
	return nil
}

// Load reads and verifies a stage checkpoint, returning its payload.
// A digest mismatch or missing file yields an ErrCheckpoint error.
func (s *Store) Load(taskKey string, stage model.Stage) ([]byte, *Checkpoint, error) {
	dir, err := s.taskDir(taskKey)
	if err != nil {
		return nil, nil, err
	}
	meta, err := os.ReadFile(filepath.Join(dir, string(stage)+".json"))
	if err != nil {
		return nil, nil, model.NewError(model.ErrCheckpoint, stage, "", -1, "no checkpoint: %v", err)
	}
	var cp Checkpoint
	if err := json.Unmarshal(meta, &cp); err != nil {
		return nil, nil, model.NewError(model.ErrCheckpoint, stage, "", -1, "checkpoint parse: %v", err)
	}
	if !cp.Done || cp.Stage != stage || cp.TaskKey != taskKey {
		return nil, nil, model.NewError(model.ErrCheckpoint, stage, "", -1, "checkpoint incomplete or mismatched")
	}
	payload, err := os.ReadFile(filepath.Join(dir, string(stage)+".bin"))
	if err != nil {
		return nil, nil, model.NewError(model.ErrCheckpoint, stage, "", -1, "payload missing: %v", err)
	}
	sum := sha256.Sum256(payload)
	if hex.EncodeToString(sum[:]) != cp.Digest || int64(len(payload)) != cp.Size {
		return nil, nil, model.NewError(model.ErrCheckpoint, stage, "", -1,
			"checkpoint digest mismatch for stage %s", stage)
	}
	return payload, &cp, nil
}

// Invalidate removes checkpoints and payloads at and after the given stage.
func (s *Store) Invalidate(taskKey string, from model.Stage) error {
	dir, err := s.taskDir(taskKey)
	if err != nil {
		return err
	}
	for _, st := range StageOrder[StageIndex(from):] {
		os.Remove(filepath.Join(dir, string(st)+".json"))
		os.Remove(filepath.Join(dir, string(st)+".bin"))
	}
	return nil
}

// LastValid returns the highest consecutive stage (from demux) whose
// checkpoint verifies, and whether any valid checkpoint exists.
func (s *Store) LastValid(taskKey string) (model.Stage, bool) {
	last := model.Stage("")
	ok := false
	for _, st := range StageOrder {
		if _, _, err := s.Load(taskKey, st); err != nil {
			break
		}
		last, ok = st, true
	}
	return last, ok
}
