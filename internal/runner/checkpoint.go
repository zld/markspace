// Package runner orchestrates the streaming pipeline, records committed
// segments under stable task keys and resumes from the last complete
// checkpoint without double counting.
package runner

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"os"
	"path/filepath"
	"sort"

	"mediasync/internal/model"
	"mediasync/internal/rtp"
)

// CheckpointVersion tags the on-disk checkpoint format.
const CheckpointVersion = 1

// Segment is one committed, resumable range of extended sequence numbers.
type Segment struct {
	StartExt uint64 `json:"start_ext"`
	EndExt   uint64 `json:"end_ext"` // inclusive
	Count    int    `json:"count"`
}

// Checkpoint is the complete resumable state of one task.
type Checkpoint struct {
	Version        int               `json:"version"`
	TaskKey        string            `json:"task_key"`
	InputSHA256    string            `json:"input_sha256"`
	ParamsDigest   string            `json:"params_digest"`
	SSRC           uint32            `json:"ssrc"`
	NextCommitExt  uint64            `json:"next_commit_ext"`
	HighestExt     uint64            `json:"highest_ext"`
	BaseExt        uint64            `json:"base_ext"`
	CommittedCount int               `json:"committed_count"`
	Duplicates     int               `json:"duplicates"`
	Late           int               `json:"late"`
	Segments       []Segment         `json:"segments"`
	Missing        []uint64          `json:"missing"`
	Seen           []uint64          `json:"seen"`
	Completed      bool              `json:"completed"`
}

func checkpointPath(dir, taskKey string) string {
	return filepath.Join(dir, "checkpoints", taskKey+".json")
}

// TaskKey derives a stable key from the input digest and parameter digest, so
// the same work always maps to the same checkpoint while changed inputs do
// not collide with prior results.
func TaskKey(inputSHA, paramsDigest string) string {
	h := sha256.Sum256([]byte(inputSHA + "|" + paramsDigest))
	return hex.EncodeToString(h[:16])
}

// DigestParams renders a canonical digest of run parameters.
func DigestParams(params map[string]any) (string, error) {
	keys := make([]string, 0, len(params))
	for k := range params {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	h := sha256.New()
	for _, k := range keys {
		h.Write([]byte(k))
		h.Write([]byte{0})
		raw, err := json.Marshal(params[k])
		if err != nil {
			return "", model.NewError(model.ClassCheckpoint, "encode param %s: %v", k, err)
		}
		h.Write(raw)
		h.Write([]byte{0})
	}
	return hex.EncodeToString(h.Sum(nil)), nil
}

// SaveCheckpoint atomically persists state.
func SaveCheckpoint(dir string, cp *Checkpoint) error {
	if cp.Version == 0 {
		cp.Version = CheckpointVersion
	}
	body, err := json.MarshalIndent(cp, "", "  ")
	if err != nil {
		return model.NewError(model.ClassCheckpoint, "encode checkpoint: %v", err)
	}
	dst := checkpointPath(dir, cp.TaskKey)
	if err := os.MkdirAll(filepath.Dir(dst), 0o755); err != nil {
		return model.NewError(model.ClassCheckpoint, "mkdir: %v", err)
	}
	tmp, err := os.CreateTemp(filepath.Dir(dst), ".cp-*.tmp")
	if err != nil {
		return model.NewError(model.ClassCheckpoint, "temp: %v", err)
	}
	name := tmp.Name()
	if _, err := tmp.Write(body); err != nil {
		_ = tmp.Close()
		_ = os.Remove(name)
		return model.NewError(model.ClassCheckpoint, "write: %v", err)
	}
	if err := tmp.Sync(); err != nil {
		_ = tmp.Close()
		_ = os.Remove(name)
		return model.NewError(model.ClassCheckpoint, "fsync: %v", err)
	}
	if err := tmp.Close(); err != nil {
		_ = os.Remove(name)
		return model.NewError(model.ClassCheckpoint, "close: %v", err)
	}
	if err := os.Rename(name, dst); err != nil {
		_ = os.Remove(name)
		return model.NewError(model.ClassCheckpoint, "rename: %v", err)
	}
	return nil
}

// LoadCheckpoint reads a checkpoint; a missing file returns (nil, nil).
func LoadCheckpoint(dir, taskKey string) (*Checkpoint, error) {
	body, err := os.ReadFile(checkpointPath(dir, taskKey))
	if os.IsNotExist(err) {
		return nil, nil
	}
	if err != nil {
		return nil, model.NewError(model.ClassCheckpoint, "read: %v", err)
	}
	var cp Checkpoint
	if err := json.Unmarshal(body, &cp); err != nil {
		return nil, model.AtError(model.ClassCheckpoint, checkpointPath(dir, taskKey), 0, 0,
			"checkpoint truncated or corrupt: %v", err)
	}
	if cp.Version != CheckpointVersion {
		return nil, model.NewError(model.ClassCheckpoint, "checkpoint version %d unsupported", cp.Version)
	}
	return &cp, nil
}

// Guard verifies a loaded checkpoint still matches the current input and
// parameters; a mismatch means resuming would corrupt counters, so the
// caller must start fresh rather than reuse foreign state.
func Guard(cp *Checkpoint, inputSHA, paramsDigest string) error {
	if cp.InputSHA256 != inputSHA {
		return model.AtError(model.ClassCheckpoint, "", cp.SSRC, -1,
			"checkpoint input %s does not match current %s", cp.InputSHA256, inputSHA)
	}
	if cp.ParamsDigest != paramsDigest {
		return model.NewError(model.ClassCheckpoint,
			"checkpoint params %s do not match current %s", cp.ParamsDigest, paramsDigest)
	}
	return nil
}

// FromStats builds checkpoint state from a live reconstructor.
func FromStats(taskKey, inputSHA, paramsDigest string, st rtp.Stats,
	nextCommit, highest, base uint64, committed int, segments []Segment,
	missing, seen []uint64) *Checkpoint {
	return &Checkpoint{
		Version:        CheckpointVersion,
		TaskKey:        taskKey,
		InputSHA256:    inputSHA,
		ParamsDigest:   paramsDigest,
		SSRC:           st.SSRC,
		NextCommitExt:  nextCommit,
		HighestExt:     highest,
		BaseExt:        base,
		CommittedCount: committed,
		Duplicates:     st.Duplicates,
		Late:           st.Late,
		Segments:       segments,
		Missing:        missing,
		Seen:           seen,
	}
}
