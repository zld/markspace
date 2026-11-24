// Package store persists pipeline progress as checkpoints and derives the
// deterministic cache keys that make stage skipping safe. A checkpoint is
// only ever visible through an atomic rename, so a process killed mid-write
// leaves the previous valid checkpoint intact.
package store

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"time"
)

// StageState is the persisted outcome of one scheduled stage.
type StageState struct {
	StageID    string `json:"stage_id"`
	Status     string `json:"status"` // "done"
	CacheKey   string `json:"cache_key"`
	InputKey   string `json:"input_key"`
	ParamsHash string `json:"params_hash"`
	Version    string `json:"version"`
	Digest     string `json:"digest"` // stage output summary, feeds dependents
	StartedAt  string `json:"started_at"`
	EndedAt    string `json:"ended_at"`
}

// Checkpoint is the full resumable state of one pipeline run.
type Checkpoint struct {
	RunKey    string                `json:"run_key"`
	UpdatedAt string                `json:"updated_at"`
	Stages    map[string]StageState `json:"stages"`
}

// Path names the checkpoint file inside a run directory.
func Path(dir string) string { return filepath.Join(dir, "checkpoint.json") }

// Load reads a checkpoint; a missing file yields an empty, usable checkpoint.
func Load(dir string) (*Checkpoint, error) {
	b, err := os.ReadFile(Path(dir))
	if os.IsNotExist(err) {
		return &Checkpoint{Stages: map[string]StageState{}}, nil
	}
	if err != nil {
		return nil, err
	}
	cp := &Checkpoint{}
	if err := json.Unmarshal(b, cp); err != nil {
		return nil, fmt.Errorf("store: corrupt checkpoint: %w", err)
	}
	if cp.Stages == nil {
		cp.Stages = map[string]StageState{}
	}
	return cp, nil
}

// Save writes the checkpoint atomically: temp file, fsync, rename. The
// staging file sits in the same directory so the rename is on one filesystem.
func Save(dir string, cp *Checkpoint) error {
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	cp.UpdatedAt = time.Now().UTC().Format(time.RFC3339Nano)
	b, err := json.MarshalIndent(cp, "", "  ")
	if err != nil {
		return err
	}
	tmp := Path(dir) + ".tmp"
	f, err := os.OpenFile(tmp, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o644)
	if err != nil {
		return err
	}
	if _, err := f.Write(b); err != nil {
		_ = f.Close()
		return err
	}
	if err := f.Sync(); err != nil {
		_ = f.Close()
		return err
	}
	if err := f.Close(); err != nil {
		return err
	}
	return os.Rename(tmp, Path(dir))
}

// MarkDone records a finished stage and persists the checkpoint in one call.
func (cp *Checkpoint) MarkDone(dir string, state StageState) error {
	state.Status = "done"
	state.EndedAt = time.Now().UTC().Format(time.RFC3339Nano)
	if state.StartedAt == "" {
		state.StartedAt = state.EndedAt
	}
	cp.Stages[state.StageID] = state
	return Save(dir, cp)
}

// hashString returns the full lowercase hex SHA-256 of s.
func hashString(s string) string {
	sum := sha256.Sum256([]byte(s))
	return hex.EncodeToString(sum[:])
}

// shortHash trims a digest to a stable 12-char prefix for readable keys.
func shortHash(s string) string { return hashString(s)[:12] }

// ParamsHash hashes an arbitrary JSON-serializable parameter block. The
// encoding is deterministic for maps because keys are sorted by encoding/json.
func ParamsHash(params interface{}) (string, error) {
	b, err := json.Marshal(params)
	if err != nil {
		return "", fmt.Errorf("store: params hash: %w", err)
	}
	return shortHash(string(b)), nil
}

// InputKey combines one or more upstream digests (checksums, prior stage
// digests) into a single stable input identity. Empty inputs hash the empty
// string so source stages still get a key.
func InputKey(digests []string) string {
	sorted := append([]string(nil), digests...)
	sort.Strings(sorted)
	return shortHash("inputs:" + joinSorted(sorted))
}

func joinSorted(s []string) string {
	out := ""
	for i, v := range s {
		if i > 0 {
			out += "|"
		}
		out += v
	}
	return out
}

// CacheKey identifies a stage execution. A stage may be skipped only when a
// prior run recorded exactly this key, i.e. identical inputs, parameters and
// stage version. Version is an explicit caller-supplied constant so changing
// stage code invalidates every cached result.
func CacheKey(stage, inputKey, paramsHash, version string) string {
	raw := fmt.Sprintf("stage=%s\ninput=%s\nparams=%s\nversion=%s",
		stage, inputKey, paramsHash, version)
	return shortHash(raw)
}

// Reusable reports whether a stage can be skipped under this checkpoint: it
// finished before and its full cache key still matches.
func (cp *Checkpoint) Reusable(stage, cacheKey string) (StageState, bool) {
	st, ok := cp.Stages[stage]
	return st, ok && st.Status == "done" && st.CacheKey == cacheKey
}

// Digest returns the recorded output digest of a finished stage, or "".
func (cp *Checkpoint) Digest(stage string) string {
	if st, ok := cp.Stages[stage]; ok {
		return st.Digest
	}
	return ""
}
