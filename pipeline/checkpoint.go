// Package pipeline orchestrates the analysis stages and persists
// checkpoints so an interrupted run resumes from the last valid stage.
package pipeline

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"os"
	"path/filepath"
	"sort"

	"streamcheck/core"
)

// CheckpointFile is the checkpoint file name inside the work directory.
const CheckpointFile = "checkpoint.json"

// Checkpoint records the input digest, the last completed stage and a
// digest of all stage results, so a resume can prove it continues the
// same run.
type Checkpoint struct {
	InputDigest  string            `json:"inputDigest"`
	Stage        string            `json:"stage"`
	ResultDigest string            `json:"resultDigest"`
	Artifacts    map[string]string `json:"artifacts"`
}

// DigestArtifacts hashes stage results deterministically.
func DigestArtifacts(a map[string]string) string {
	keys := make([]string, 0, len(a))
	for k := range a {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	h := sha256.New()
	for _, k := range keys {
		h.Write([]byte(k))
		h.Write([]byte{0})
		h.Write([]byte(a[k]))
		h.Write([]byte{0})
	}
	return hex.EncodeToString(h.Sum(nil))
}

// DigestInputs hashes the named input files (relative to work) into one
// digest covering names and contents in sorted order.
func DigestInputs(work string, inputs []string) (string, error) {
	names := append([]string(nil), inputs...)
	sort.Strings(names)
	h := sha256.New()
	for _, name := range names {
		data, err := os.ReadFile(filepath.Join(work, name))
		if err != nil {
			return "", core.E(core.KindInput, "digest", "cannot read input", err).At(name, -1)
		}
		h.Write([]byte(name))
		h.Write([]byte{0})
		h.Write(data)
		h.Write([]byte{0})
	}
	return hex.EncodeToString(h.Sum(nil)), nil
}

// SaveCheckpoint writes the checkpoint atomically.
func SaveCheckpoint(work string, cp Checkpoint) error {
	data, err := json.MarshalIndent(cp, "", "  ")
	if err != nil {
		return core.E(core.KindCheckpoint, "save", "cannot encode checkpoint", err)
	}
	tmp := filepath.Join(work, CheckpointFile+".tmp")
	if err := os.WriteFile(tmp, data, 0o644); err != nil {
		return core.E(core.KindCheckpoint, "save", "cannot write checkpoint", err).At(tmp, -1)
	}
	if err := os.Rename(tmp, filepath.Join(work, CheckpointFile)); err != nil {
		return core.E(core.KindCheckpoint, "save", "cannot commit checkpoint", err)
	}
	return nil
}

// LoadCheckpoint reads and verifies the checkpoint. A checkpoint whose
// result digest does not match its artifacts is corrupt and rejected.
func LoadCheckpoint(work string) (*Checkpoint, error) {
	path := filepath.Join(work, CheckpointFile)
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, core.E(core.KindCheckpoint, "load", "no usable checkpoint", err).At(path, -1)
	}
	var cp Checkpoint
	if err := json.Unmarshal(data, &cp); err != nil {
		return nil, core.E(core.KindCheckpoint, "load", "corrupt checkpoint", err).At(path, -1)
	}
	if cp.ResultDigest != DigestArtifacts(cp.Artifacts) {
		return nil, core.E(core.KindCheckpoint, "load", "checkpoint digest mismatch", nil).At(path, -1)
	}
	return &cp, nil
}
