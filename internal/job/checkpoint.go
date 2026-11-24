// Package job orchestrates idempotent recovery runs with resumable
// checkpoints. Parsing stays streaming; checkpoints bind results to the
// exact input bytes, configuration and adapter version that produced them.
package job

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"os"
	"path/filepath"

	"archive-recovery/internal/media"
)

// CheckpointSchemaVersion is bumped on incompatible layout changes.
const CheckpointSchemaVersion = 1

// Job statuses.
const (
	StatusInterrupted = "interrupted"
	StatusComplete    = "complete"
)

// Checkpoint is the durable resumption state.
type Checkpoint struct {
	SchemaVersion  int      `json:"schema_version"`
	InputSHA256    string   `json:"input_sha256"`
	ConfigDigest   string   `json:"config_digest"`
	Codec          string   `json:"codec"`
	AdapterVersion string   `json:"adapter_version"`
	ChunkBytes     int64    `json:"chunk_bytes"`
	TotalChunks    int      `json:"total_chunks"`
	Completed      []int    `json:"completed_chunks"`
	ChunkDigests   []string `json:"chunk_digests"` // index-aligned with chunk number
	Status         string   `json:"status"`
}

// ConfigDigest canonicalizes the run configuration into a 64-hex digest so
// any parameter change invalidates a previously stored checkpoint.
func ConfigDigest(codec string, chunkBytes int64, seed int64) string {
	h := sha256.New()
	h.Write([]byte(codec))
	var buf [8]byte
	for _, v := range []int64{chunkBytes, seed} {
		for i := 0; i < 8; i++ {
			buf[i] = byte(v >> (8 * i))
		}
		h.Write(buf[:])
	}
	return hex.EncodeToString(h.Sum(nil))
}

// SaveCheckpoint writes the checkpoint atomically (temp file + rename) so a
// crash can never leave a half-written state file.
func SaveCheckpoint(dir string, cp *Checkpoint) error {
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return media.WrapError(media.ClassCheckpoint, "job", "checkpoint dir", err)
	}
	path := filepath.Join(dir, "checkpoint.json")
	tmp := path + ".tmp"
	raw, err := json.MarshalIndent(cp, "", "  ")
	if err != nil {
		return media.WrapError(media.ClassCheckpoint, "job", "checkpoint encode", err)
	}
	if err := os.WriteFile(tmp, raw, 0o644); err != nil {
		return media.WrapError(media.ClassCheckpoint, "job", "checkpoint write", err)
	}
	if err := os.Rename(tmp, path); err != nil {
		return media.WrapError(media.ClassCheckpoint, "job", "checkpoint rename", err)
	}
	return nil
}

// LoadCheckpoint reads and minimally validates a checkpoint file. A missing
// file returns (nil, nil) so callers can start a fresh job.
func LoadCheckpoint(dir string) (*Checkpoint, error) {
	path := filepath.Join(dir, "checkpoint.json")
	raw, err := os.ReadFile(path)
	if os.IsNotExist(err) {
		return nil, nil
	}
	if err != nil {
		return nil, media.WrapError(media.ClassCheckpoint, "job", "checkpoint read", err)
	}
	var cp Checkpoint
	if err := json.Unmarshal(raw, &cp); err != nil {
		return nil, media.WrapError(media.ClassCheckpoint, "job", "checkpoint decode", err)
	}
	if cp.SchemaVersion != CheckpointSchemaVersion {
		return nil, media.NewError(media.ClassCheckpoint, "job",
			"checkpoint schema version mismatch")
	}
	return &cp, nil
}

// Validate refuses to reuse a checkpoint whose input, configuration or
// adapter identity differs from the current run. The on-disk state is left
// untouched so a compatible future version can still resume it.
func (cp *Checkpoint) Validate(inputSHA, configDigest, adapterVersion string) error {
	if cp.Status != StatusInterrupted {
		return media.NewError(media.ClassCheckpoint, "job",
			"checkpoint is not resumable (status="+cp.Status+")")
	}
	if cp.InputSHA256 != inputSHA {
		return media.NewError(media.ClassCheckpoint, "job", "input changed since checkpoint")
	}
	if cp.ConfigDigest != configDigest {
		return media.NewError(media.ClassCheckpoint, "job", "configuration changed since checkpoint")
	}
	if cp.AdapterVersion != adapterVersion {
		return media.NewError(media.ClassCheckpoint, "job",
			"adapter version changed since checkpoint: "+cp.AdapterVersion+" -> "+adapterVersion)
	}
	return nil
}

// IsCompleted reports whether chunk i was verified in an earlier run.
func (cp *Checkpoint) IsCompleted(i int) bool {
	for _, c := range cp.Completed {
		if c == i {
			return true
		}
		if c > i {
			return false
		}
	}
	return false
}
