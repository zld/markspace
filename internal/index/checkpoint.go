package index

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"sort"

	"replaycheck/internal/clock"
)

// Checkpoint is the recovery point written after a fixed number of blocks.
type Checkpoint struct {
	BasisVersion       string           `json:"basis_version"`
	CheckpointEvery    int              `json:"checkpoint_every"`
	InputsDigest       string           `json:"inputs_digest"`
	Inputs             []InputSummary   `json:"inputs"`
	CompletedBlocks    []BlockRecord    `json:"completed_blocks"`
	ConfirmedBlockKeys []string         `json:"confirmed_block_keys"`
	Conclusions        []FileConclusion `json:"conclusions,omitempty"`
	Anomalies          []clock.Anomaly  `json:"anomalies,omitempty"`
	LastBlockKey       string           `json:"last_block_key"`
	CompletedCount     int              `json:"completed_count"`
}

// InputsDigest computes a stable digest over path/size/SHA tuples. Input order
// is normalized so directory traversal order cannot change the result.
func InputsDigest(inputs []InputSummary) string {
	cp := append([]InputSummary(nil), inputs...)
	sort.Slice(cp, func(i, j int) bool { return cp[i].Path < cp[j].Path })
	b, _ := json.Marshal(cp)
	sum := sha256.Sum256(b)
	return hex.EncodeToString(sum[:])
}

// SaveCheckpoint atomically writes a checkpoint.
func SaveCheckpoint(path string, cp Checkpoint) error {
	if cp.BasisVersion == "" {
		cp.BasisVersion = clock.BasisVersion
	}
	cp.InputsDigest = InputsDigest(cp.Inputs)
	b, err := json.MarshalIndent(cp, "", "  ")
	if err != nil {
		return err
	}
	b = append(b, '\n')
	return atomicWrite(path, b)
}

// LoadCheckpoint reads a checkpoint.
func LoadCheckpoint(path string) (*Checkpoint, error) {
	b, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	var cp Checkpoint
	if err := json.Unmarshal(b, &cp); err != nil {
		return nil, clock.NewError(clock.CodeCheckpointMismatch, "invalid checkpoint JSON: "+err.Error(), -1)
	}
	return &cp, nil
}

// VerifyInputs rejects resume when current input summaries differ.
func VerifyInputs(cp *Checkpoint, current []InputSummary) error {
	want := InputsDigest(cp.Inputs)
	got := InputsDigest(current)
	if cp.InputsDigest != "" && cp.InputsDigest != want {
		return clock.NewError(clock.CodeCheckpointMismatch,
			fmt.Sprintf("stored checkpoint digest %s does not match its inputs %s", cp.InputsDigest, want), -1)
	}
	if want != got {
		return clock.NewError(clock.CodeDigestMismatch,
			fmt.Sprintf("input summary digest changed: checkpoint=%s current=%s; refusing resume", want, got), -1)
	}
	if cp.BasisVersion != clock.BasisVersion {
		return clock.NewError(clock.CodeCheckpointMismatch,
			fmt.Sprintf("time basis version changed: checkpoint=%s current=%s", cp.BasisVersion, clock.BasisVersion), -1)
	}
	return nil
}

// IsConfirmed reports whether a block key was committed in the checkpoint.
func (cp *Checkpoint) IsConfirmed(key string) bool {
	for _, k := range cp.ConfirmedBlockKeys {
		if k == key {
			return true
		}
	}
	return false
}
