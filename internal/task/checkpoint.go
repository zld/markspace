package task

import (
	"audionorm/internal/audio"
	"audionorm/internal/media"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
)

// Checkpoint is the on-disk recovery record for one task. It is written
// atomically after every successful stage transition.
type Checkpoint struct {
	TaskKey        string                 `json:"task_key"`
	StreamID       string                 `json:"stream_id"`
	Range          media.Range            `json:"range"`
	Spec           Spec                   `json:"spec"`
	Stage          Stage                  `json:"stage"`
	Status         Status                 `json:"status"`
	InputSHA256    string                 `json:"input_sha256"`
	DecoderVersion string                 `json:"decoder_version"`
	Stages         map[Stage]*StageResult `json:"stages"`
	// Metrics captured at normalize/seal time for the manifest.
	Samples        int                   `json:"samples"`
	MissingSamples uint64                `json:"missing_samples"`
	BudgetDevPct   float64               `json:"budget_deviation_pct"`
	Loudness       *audio.LoudnessReport `json:"loudness,omitempty"`
	Errors         []*media.MediaError   `json:"errors,omitempty"`
}

const checkpointFile = "checkpoint.json"

func hashReader(r io.Reader) (string, error) {
	h := sha256.New()
	if _, err := io.Copy(h, r); err != nil {
		return "", err
	}
	return hex.EncodeToString(h.Sum(nil)), nil
}

// TaskDir is the on-disk directory for one task key under the work root.
func TaskDir(root, key string) string {
	return filepath.Join(root, "tasks", key)
}

// SaveCheckpoint writes the checkpoint atomically and returns its hash.
func SaveCheckpoint(root string, cp *Checkpoint) (string, error) {
	dir := TaskDir(root, cp.TaskKey)
	return AtomicWrite(dir, checkpointFile, cp)
}

// LoadCheckpoint reads and structurally validates a checkpoint. It does
// NOT verify payload hashes; call Verify for that before reusing stages.
func LoadCheckpoint(root, key string) (*Checkpoint, error) {
	path := filepath.Join(TaskDir(root, key), checkpointFile)
	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, ErrCheckpointNotFound
		}
		return nil, err
	}
	var cp Checkpoint
	if err := json.Unmarshal(data, &cp); err != nil {
		return nil, fmt.Errorf("%w: %v", media.ErrSentinelCheckpoint, err)
	}
	if cp.TaskKey != key {
		return nil, errors.New("checkpoint task key mismatch")
	}
	if cp.Stages == nil {
		cp.Stages = map[Stage]*StageResult{}
	}
	return &cp, nil
}

// Verify re-hashes every persisted stage payload and confirms the
// decoder version. The first failing stage and all later stages are
// invalidated. It returns the highest stage that may be reused.
func (cp *Checkpoint) Verify(root string) (Stage, error) {
	dir := TaskDir(root, cp.TaskKey)
	reusable := StageNew
	for _, st := range stageOrder[1:] {
		res, ok := cp.Stages[st]
		if !ok {
			break
		}
		if res.DecoderVersion != audio.DecoderVersion {
			return reusable, fmt.Errorf("%w: stage %s decoder version changed",
				media.ErrSentinelCheckpoint, st)
		}
		got, err := HashFile(filepath.Join(dir, res.PayloadFile))
		if err != nil {
			return reusable, fmt.Errorf("%w: stage %s payload missing: %v",
				media.ErrSentinelCheckpoint, st, err)
		}
		if got != res.PayloadSHA256 {
			return reusable, fmt.Errorf("%w: stage %s hash mismatch",
				media.ErrSentinelCheckpoint, st)
		}
		reusable = st
	}
	return reusable, nil
}

// InputChanged reports whether the supplied input hash differs from the
// checkpoint's recorded input. A changed input invalidates the whole
// pipeline because every downstream artifact derives from it.
func (cp *Checkpoint) InputChanged(inputSHA string) bool {
	return cp.InputSHA256 != inputSHA
}

// CheckConflict enforces the stable-key rule: same key must carry the
// same input. A different input is a hard conflict, never an overwrite.
func CheckConflict(existing *Checkpoint, streamID string, r media.Range, spec Spec, inputSHA string) error {
	if existing.InputSHA256 == inputSHA &&
		existing.StreamID == streamID && existing.Range == r && existing.Spec == spec {
		return nil
	}
	return fmt.Errorf("%w: task key %s already exists with different input",
		media.ErrSentinelConflict, existing.TaskKey)
}

// RecordStage atomically persists a stage payload and updates the
// checkpoint map before saving it.
func (cp *Checkpoint) RecordStage(root string, st Stage, payloadFile string, payload []byte) error {
	if !CanAdvance(cp.Stage, st) && cp.Stage != st {
		return fmt.Errorf("illegal stage transition %s -> %s", cp.Stage, st)
	}
	dir := TaskDir(root, cp.TaskKey)
	hash, err := AtomicWriteBytes(dir, payloadFile, payload)
	if err != nil {
		return err
	}
	cp.Stages[st] = &StageResult{
		Stage: st, PayloadFile: payloadFile, PayloadSHA256: hash,
		DecoderVersion: audio.DecoderVersion,
	}
	cp.Stage = st
	if _, err := SaveCheckpoint(root, cp); err != nil {
		return err
	}
	return nil
}
