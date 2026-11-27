package index

import (
	"encoding/json"
	"os"
	"path/filepath"

	"tracecheck/internal/serr"
)

// Checkpoint records how far a batch has durably progressed. It is written
// atomically after every committed chunk so a crash leaves either the previous
// or the new checkpoint, never a torn file.
type Checkpoint struct {
	Version         int      `json:"version"`
	InputPath       string   `json:"input_path"`
	InputSHA256     string   `json:"input_sha256"`
	InputSize       int64    `json:"input_size"`
	CompletedOffset int64    `json:"completed_offset"`
	CompletedChunks int      `json:"completed_chunks"`
	TotalChunks     int      `json:"total_chunks"`
	IndexDigest     string   `json:"index_digest"`
	MetadataDigest  string   `json:"metadata_digest"`
	SegmentHashes   []string `json:"segment_hashes"`
	ChunkSize       int      `json:"chunk_size"`
	LastComplete    bool     `json:"last_complete"`
}

// CheckpointVersion is the on-disk schema version written by this build.
const CheckpointVersion = 1

// CheckpointPath returns the conventional checkpoint file name for an input.
func CheckpointPath(workDir, inputName string) string {
	return filepath.Join(workDir, inputName+".checkpoint.json")
}

// SaveCheckpoint writes cp atomically to path: a temp file in the same
// directory is fsynced and renamed over the destination.
func SaveCheckpoint(path string, cp *Checkpoint) error {
	cp.Version = CheckpointVersion
	data, err := json.MarshalIndent(cp, "", "  ")
	if err != nil {
		return serr.New(serr.CodeIO, "encode checkpoint: %v", err)
	}
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return serr.New(serr.CodeIO, "create checkpoint dir %s: %v", dir, err)
	}
	tmp, err := os.CreateTemp(dir, ".checkpoint-*.tmp")
	if err != nil {
		return serr.New(serr.CodeIO, "create temp checkpoint: %v", err)
	}
	tmpName := tmp.Name()
	defer os.Remove(tmpName)
	if _, err := tmp.Write(data); err != nil {
		tmp.Close()
		return serr.New(serr.CodeIO, "write checkpoint: %v", err)
	}
	if err := tmp.Sync(); err != nil {
		tmp.Close()
		return serr.New(serr.CodeIO, "fsync checkpoint: %v", err)
	}
	if err := tmp.Close(); err != nil {
		return serr.New(serr.CodeIO, "close checkpoint: %v", err)
	}
	if err := os.Rename(tmpName, path); err != nil {
		return serr.New(serr.CodeIO, "rename checkpoint: %v", err)
	}
	return nil
}

// LoadCheckpoint reads and validates a checkpoint file. A missing file returns
// (nil, nil) so a first run is not an error. Any decode failure, schema
// mismatch, negative offset or out-of-range field returns CodeCheckpointCorrupt.
func LoadCheckpoint(path string) (*Checkpoint, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, serr.New(serr.CodeIO, "read checkpoint %s: %v", path, err)
	}
	var cp Checkpoint
	if err := json.Unmarshal(data, &cp); err != nil {
		return nil, serr.New(serr.CodeCheckpointCorrupt, "checkpoint %s is not valid JSON: %v", path, err)
	}
	if err := cp.validate(); err != nil {
		return nil, err
	}
	return &cp, nil
}

// validate enforces the structural invariants that a resume relies on.
func (cp *Checkpoint) validate() error {
	switch {
	case cp.Version != CheckpointVersion:
		return serr.New(serr.CodeCheckpointCorrupt,
			"checkpoint version %d, want %d", cp.Version, CheckpointVersion)
	case cp.InputPath == "":
		return serr.New(serr.CodeCheckpointCorrupt, "checkpoint has no input path")
	case cp.InputSHA256 == "":
		return serr.New(serr.CodeCheckpointCorrupt, "checkpoint has no input digest")
	case cp.InputSize < 0:
		return serr.New(serr.CodeCheckpointCorrupt, "checkpoint input size %d is negative", cp.InputSize)
	case cp.CompletedOffset < 0 || cp.CompletedOffset > cp.InputSize:
		return serr.New(serr.CodeCheckpointCorrupt,
			"checkpoint completed offset %d is outside [0,%d]", cp.CompletedOffset, cp.InputSize)
	case cp.CompletedChunks < 0 || cp.TotalChunks < 0 || cp.CompletedChunks > cp.TotalChunks:
		return serr.New(serr.CodeCheckpointCorrupt,
			"checkpoint chunk counts are inconsistent: %d of %d",
			cp.CompletedChunks, cp.TotalChunks)
	case cp.ChunkSize <= 0:
		return serr.New(serr.CodeCheckpointCorrupt, "checkpoint chunk size %d is not positive", cp.ChunkSize)
	case len(cp.SegmentHashes) != cp.CompletedChunks:
		return serr.New(serr.CodeCheckpointCorrupt,
			"checkpoint has %d segment hashes for %d completed chunks",
			len(cp.SegmentHashes), cp.CompletedChunks)
	}
	return nil
}

// VerifyAgainst checks a loaded checkpoint against the input actually being
// processed. A digest, size or chunk-size mismatch means the checkpoint
// belongs to different bytes and must not be resumed from.
func (cp *Checkpoint) VerifyAgainst(inputSHA256 string, inputSize int64, chunkSize int) error {
	if cp.InputSHA256 != inputSHA256 {
		return serr.New(serr.CodeCheckpointCorrupt,
			"checkpoint input digest %s does not match %s", cp.InputSHA256, inputSHA256)
	}
	if cp.InputSize != inputSize {
		return serr.New(serr.CodeCheckpointCorrupt,
			"checkpoint input size %d does not match %d", cp.InputSize, inputSize)
	}
	if cp.ChunkSize != chunkSize {
		return serr.New(serr.CodeCheckpointCorrupt,
			"checkpoint chunk size %d does not match %d", cp.ChunkSize, chunkSize)
	}
	return nil
}
