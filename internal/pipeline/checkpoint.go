// Package pipeline ties parsing, repair, indexing, caching and evidence into
// one resumable run. Progress is checkpointed so an interrupted run continues
// from the last fragment without duplicating or skipping artifacts.
package pipeline

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"

	"rtx/internal/model"
)

// CheckpointVersion is part of the checkpoint file; a mismatch is a hard error
// rather than a silent reset, because continuing with incompatible state could
// duplicate or skip fragments.
const CheckpointVersion = 1

// Code values.
const (
	// CodeCheckpoint marks an unusable checkpoint (bad JSON, wrong version,
	// or a manifest that no longer matches the recorded digest).
	CodeCheckpoint model.Code = "checkpoint_invalid"
	// CodeResumeMismatch marks a checkpoint whose output contents do not match
	// what was recorded when the cursor advanced.
	CodeResumeMismatch model.Code = "checkpoint_resume_mismatch"
)

// ArtifactRecord is a landed output file and its digest at cursor time.
type ArtifactRecord struct {
	Name   string `json:"name"`
	SHA256 string `json:"sha256"`
	Size   int64  `json:"size"`
}

// Checkpoint is the durable run state. The derived timeline and index are
// stored so a resumed run continues from the same repair state without
// re-deriving it differently; the input digest guarantees they still apply.
type Checkpoint struct {
	Version int    `json:"version"`
	RunID   string `json:"run_id"`
	// ManifestSum binds the checkpoint to the exact input list.
	ManifestSum string `json:"manifest_sha256"`
	// InputSum binds it to the exact input bytes (manifest + all fragments).
	InputSum string `json:"input_sha256"`
	// ParamsSum binds it to the processing parameters.
	ParamsSum string `json:"params_sha256"`
	// Cursor is the number of fragment records fully processed and landed.
	Cursor int `json:"cursor"`
	// Total is the number of fragment records in the manifest.
	Total int `json:"total"`
	// Timeline and Index are identical to the uninterrupted result at any
	// cursor, because they are derived from the full input, not the prefix.
	Timeline json.RawMessage `json:"timeline,omitempty"`
	Index    json.RawMessage `json:"index,omitempty"`
	// Artifacts are outputs already on disk for the current cursor.
	Artifacts []ArtifactRecord `json:"artifacts"`
	// Completed means the run finished and outputs are final.
	Completed bool `json:"completed"`
}

// DigestBytes is the hex SHA-256 of data.
func DigestBytes(data []byte) string {
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:])
}

// FileDigest returns the hex SHA-256 and size of a file.
func FileDigest(path string) (string, int64, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return "", 0, err
	}
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:]), int64(len(data)), nil
}

// LoadCheckpoint reads a checkpoint file. A missing file is not an error; it
// returns (nil, nil) so callers start fresh.
func LoadCheckpoint(path string) (*Checkpoint, error) {
	data, err := os.ReadFile(path)
	if os.IsNotExist(err) {
		return nil, nil
	}
	if err != nil {
		return nil, model.E(CodeCheckpoint, "load checkpoint", "%v", err).WithPath(path).WithCause(err)
	}
	var cp Checkpoint
	if err := json.Unmarshal(data, &cp); err != nil {
		return nil, model.E(CodeCheckpoint, "parse checkpoint", "%v", err).WithPath(path).WithCause(err)
	}
	if cp.Version != CheckpointVersion {
		return nil, model.E(CodeCheckpoint, "parse checkpoint",
			"version %d is not supported (want %d)", cp.Version, CheckpointVersion).WithPath(path)
	}
	if cp.Cursor < 0 || cp.Cursor > cp.Total {
		return nil, model.E(CodeCheckpoint, "parse checkpoint",
			"cursor %d is outside [0,%d]", cp.Cursor, cp.Total).WithPath(path)
	}
	return &cp, nil
}

// WriteCheckpoint atomically replaces the checkpoint file: write a temp file,
// fsync it, rename over the target, then fsync the directory. A crash leaves
// either the old or the new checkpoint, never a partial one.
func WriteCheckpoint(path string, cp *Checkpoint) error {
	cp.Version = CheckpointVersion
	data, err := json.MarshalIndent(cp, "", "  ")
	if err != nil {
		return model.E(CodeCheckpoint, "encode checkpoint", "%v", err)
	}
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return model.E(CodeCheckpoint, "write checkpoint", "%v", err).WithPath(path)
	}
	tmp, err := os.CreateTemp(dir, ".ckpt-*")
	if err != nil {
		return model.E(CodeCheckpoint, "write checkpoint", "%v", err).WithPath(path)
	}
	tmpName := tmp.Name()
	if _, err := tmp.Write(data); err != nil {
		tmp.Close()
		os.Remove(tmpName)
		return model.E(CodeCheckpoint, "write checkpoint", "%v", err).WithPath(path)
	}
	if err := tmp.Sync(); err != nil {
		tmp.Close()
		os.Remove(tmpName)
		return model.E(CodeCheckpoint, "write checkpoint", "%v", err).WithPath(path)
	}
	if err := tmp.Close(); err != nil {
		os.Remove(tmpName)
		return model.E(CodeCheckpoint, "write checkpoint", "%v", err).WithPath(path)
	}
	if err := os.Rename(tmpName, path); err != nil {
		os.Remove(tmpName)
		return model.E(CodeCheckpoint, "write checkpoint", "%v", err).WithPath(path)
	}
	if d, err := os.Open(dir); err == nil {
		_ = d.Sync()
		d.Close()
	}
	return nil
}

// VerifyArtifacts re-hashes outputs recorded in the checkpoint. A missing or
// changed artifact invalidates the cursor, so the caller reprocesses from the
// last consistent point instead of appending to corrupt output.
func VerifyArtifacts(outDir string, records []ArtifactRecord) error {
	for _, r := range records {
		path := filepath.Join(outDir, r.Name)
		sum, size, err := FileDigest(path)
		if err != nil {
			return model.E(CodeResumeMismatch, "verify artifact",
				"recorded artifact %s is missing: %v", r.Name, err).WithPath(path)
		}
		if sum != r.SHA256 || size != r.Size {
			return model.E(CodeResumeMismatch, "verify artifact",
				"artifact %s changed since checkpoint (sha %s/%s size %d/%d)", r.Name, sum, r.SHA256, size, r.Size).
				WithPath(path)
		}
	}
	return nil
}

// ResumeDecision explains what a load found so the CLI can print resume=1.
type ResumeDecision struct {
	Resuming bool
	Cursor   int
	Reason   string
}

// Reconcile applies a loaded checkpoint against the current input. It returns
// the cursor to start from; a checkpoint whose input identity differs is an
// error, not a silent restart, because restarting would duplicate outputs.
func Reconcile(cp *Checkpoint, manifestSum, inputSum, paramsSum string, total int) (ResumeDecision, error) {
	if cp == nil {
		return ResumeDecision{Cursor: 0, Reason: "fresh"}, nil
	}
	if cp.ManifestSum != manifestSum {
		return ResumeDecision{}, model.E(CodeCheckpoint, "reconcile",
			"checkpoint was written for a different manifest")
	}
	if cp.InputSum != inputSum {
		return ResumeDecision{}, model.E(CodeCheckpoint, "reconcile",
			"checkpoint input digest %s does not match current %s", cp.InputSum, inputSum)
	}
	if cp.ParamsSum != paramsSum {
		return ResumeDecision{}, model.E(CodeCheckpoint, "reconcile",
			"checkpoint processing parameters changed")
	}
	if cp.Total != total {
		return ResumeDecision{}, model.E(CodeCheckpoint, "reconcile",
			"checkpoint covers %d fragments but input has %d", cp.Total, total)
	}
	if cp.Completed {
		// A completed checkpoint still means this invocation continued from
		// existing state rather than starting fresh, so report it as a resume.
		return ResumeDecision{Resuming: true, Cursor: cp.Cursor, Reason: "already_completed"}, nil
	}
	return ResumeDecision{Resuming: cp.Cursor > 0, Cursor: cp.Cursor, Reason: fmt.Sprintf("resume_from_%d", cp.Cursor)}, nil
}
