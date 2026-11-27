// Package provenance records, for every produced result, exactly which
// inputs, index versions, parameters and checkpoints produced it, together
// with every structured error encountered.
package provenance

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"os"
	"path/filepath"

	"mediasync/internal/model"
)

// ManifestVersion tags the manifest format.
const ManifestVersion = 1

// Manifest is the independently verifiable provenance record of one run.
type Manifest struct {
	Version        int                `json:"version"`
	TaskKey        string             `json:"task_key"`
	InputSHA256    string             `json:"input_sha256"`
	IndexSHA256    string             `json:"index_sha256"`
	ParamsDigest   string             `json:"params_digest"`
	CheckpointPath string             `json:"checkpoint_path,omitempty"`
	ResultPath     string             `json:"result_path,omitempty"`
	ResultSHA256   string             `json:"result_sha256,omitempty"`
	Errors         []model.MediaError `json:"errors,omitempty"`
	Resumed        bool               `json:"resumed"`
}

// New builds a manifest from the component digests.
func New(taskKey, inputSHA, indexSHA, paramsDigest string) *Manifest {
	return &Manifest{
		Version:      ManifestVersion,
		TaskKey:      taskKey,
		InputSHA256:  inputSHA,
		IndexSHA256:  indexSHA,
		ParamsDigest: paramsDigest,
	}
}

// AttachResult hashes the already-written result file and records it.
func (m *Manifest) AttachResult(dir, resultName string) error {
	full := filepath.Join(dir, resultName)
	raw, err := os.ReadFile(full)
	if err != nil {
		return model.NewError(model.ClassCheckpoint, "read result for hashing: %v", err)
	}
	sum := sha256.Sum256(raw)
	m.ResultPath = resultName
	m.ResultSHA256 = hex.EncodeToString(sum[:])
	return nil
}

// AddError appends a structured error (no-ops on nil).
func (m *Manifest) AddError(err error) {
	if err == nil {
		return
	}
	if me := model.AsMediaError(err); me != nil {
		m.Errors = append(m.Errors, *me)
		return
	}
	m.Errors = append(m.Errors, model.MediaError{Class: "other", Offset: -1, Message: err.Error()})
}

// WriteAtomic persists the manifest next to the result.
func (m *Manifest) WriteAtomic(dir, name string) (string, error) {
	dst := filepath.Join(dir, name)
	body, err := json.MarshalIndent(m, "", "  ")
	if err != nil {
		return "", model.NewError(model.ClassCheckpoint, "encode manifest: %v", err)
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return "", model.NewError(model.ClassCheckpoint, "mkdir: %v", err)
	}
	tmp, err := os.CreateTemp(dir, ".manifest-*.tmp")
	if err != nil {
		return "", model.NewError(model.ClassCheckpoint, "temp: %v", err)
	}
	tmpName := tmp.Name()
	if _, err := tmp.Write(body); err != nil {
		_ = tmp.Close()
		_ = os.Remove(tmpName)
		return "", model.NewError(model.ClassCheckpoint, "write: %v", err)
	}
	if err := tmp.Sync(); err != nil {
		_ = tmp.Close()
		_ = os.Remove(tmpName)
		return "", model.NewError(model.ClassCheckpoint, "fsync: %v", err)
	}
	if err := tmp.Close(); err != nil {
		_ = os.Remove(tmpName)
		return "", model.NewError(model.ClassCheckpoint, "close: %v", err)
	}
	if err := os.Rename(tmpName, dst); err != nil {
		_ = os.Remove(tmpName)
		return "", model.NewError(model.ClassCheckpoint, "rename: %v", err)
	}
	sum := sha256.Sum256(body)
	return hex.EncodeToString(sum[:]), nil
}

// Load reads a manifest back for verification.
func Load(path string) (*Manifest, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return nil, model.NewError(model.ClassCheckpoint, "read manifest: %v", err)
	}
	var m Manifest
	if err := json.Unmarshal(raw, &m); err != nil {
		return nil, model.NewError(model.ClassIndexCorrupt, "manifest corrupt: %v", err)
	}
	return &m, nil
}

// VerifyResult re-hashes the referenced result and compares it to the
// recorded digest, enabling independent source verification.
func (m *Manifest) VerifyResult(dir string) error {
	raw, err := os.ReadFile(filepath.Join(dir, m.ResultPath))
	if err != nil {
		return model.NewError(model.ClassIndexCorrupt, "read result: %v", err)
	}
	sum := sha256.Sum256(raw)
	if got := hex.EncodeToString(sum[:]); got != m.ResultSHA256 {
		return model.NewError(model.ClassIndexCorrupt,
			"result hash mismatch: manifest=%s recomputed=%s", m.ResultSHA256, got)
	}
	return nil
}
