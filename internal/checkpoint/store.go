// Package checkpoint atomically tracks entries whose index and provenance are durable.
package checkpoint

import (
	"crypto/sha256"
	"encoding/hex"
	"os"
	"path/filepath"

	"mediacore/internal/diagnostic"
	"mediacore/internal/provenance"
)

const Version = "checkpoint-v1"

type EntryState struct {
	EntryNumber    int    `json:"entry_number"`
	StableKey      string `json:"stable_key"`
	IndexPath      string `json:"index_path"`
	ProvenancePath string `json:"provenance_path"`
	IndexDigest    string `json:"index_digest"`
}

type Checkpoint struct {
	Version      string                `json:"version"`
	CheckpointID string                `json:"checkpoint_id"`
	InputDigest  string                `json:"input_digest"`
	ConfigDigest string                `json:"config_digest"`
	Entries      map[string]EntryState `json:"entries"`
}

func Path(workDir string) string {
	return filepath.Join(workDir, "checkpoint.json")
}

func Identity(inputDigest, configDigest string) string {
	sum := sha256.Sum256([]byte(inputDigest + "|" + configDigest))
	return hex.EncodeToString(sum[:])
}

// New returns an empty checkpoint for the exact input/config identity.
func New(inputDigest, configDigest string) *Checkpoint {
	return &Checkpoint{
		Version:      Version,
		CheckpointID: Identity(inputDigest, configDigest),
		InputDigest:  inputDigest,
		ConfigDigest: configDigest,
		Entries:      map[string]EntryState{},
	}
}

// Load reads a checkpoint. A missing file yields a new checkpoint. Any input or
// configuration digest mismatch is an explicit exit-3 error.
func Load(path, inputDigest, configDigest string) (*Checkpoint, error) {
	cp := New(inputDigest, configDigest)
	if _, err := os.Stat(path); err != nil {
		if os.IsNotExist(err) {
			return cp, nil
		}
		return nil, err
	}
	if err := provenance.ReadJSON(path, cp); err != nil {
		return nil, diagnostic.Wrap(diagnostic.ErrCheckpoint, diagnostic.Location{Stage: "checkpoint", ManifestPath: path, ByteOffset: 0}, err)
	}
	wantID := Identity(inputDigest, configDigest)
	if cp.Version != Version || cp.InputDigest != inputDigest || cp.ConfigDigest != configDigest || cp.CheckpointID != wantID {
		return nil, diagnostic.New(diagnostic.ErrCheckpoint, diagnostic.Location{
			Stage: "checkpoint", ManifestPath: path, ByteOffset: 0, Field: "identity",
		}, "checkpoint input/config identity does not match current run")
	}
	if cp.Entries == nil {
		cp.Entries = map[string]EntryState{}
	}
	return cp, nil
}

func (c *Checkpoint) IsDone(stableKey string) bool {
	_, ok := c.Entries[stableKey]
	return ok
}

// Mark replaces the state under a stable key, making reruns idempotent.
func (c *Checkpoint) Mark(state EntryState) {
	c.Entries[state.StableKey] = state
}

func (c *Checkpoint) Save(path string) error {
	if err := provenance.WriteAtomicJSON(path, c); err != nil {
		return diagnostic.Wrap(nil, diagnostic.Location{Stage: "checkpoint", ManifestPath: path, ByteOffset: -1}, err)
	}
	return nil
}

// CompletedNumbers returns entry numbers in ascending order for reports/tests.
func (c *Checkpoint) CompletedNumbers() []int {
	seen := map[int]bool{}
	var out []int
	for _, e := range c.Entries {
		if !seen[e.EntryNumber] {
			seen[e.EntryNumber] = true
			out = append(out, e.EntryNumber)
		}
	}
	for i := 0; i < len(out); i++ {
		for j := i + 1; j < len(out); j++ {
			if out[j] < out[i] {
				out[i], out[j] = out[j], out[i]
			}
		}
	}
	return out
}
