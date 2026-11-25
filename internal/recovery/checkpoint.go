// Package recovery implements per-layer boundary checkpoints, atomic publish,
// and rollback to the previous release.
package recovery

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"

	"mediapack/internal/fault"
	"mediapack/internal/index"
)

// LayerState is the newest durable complete boundary for one representation.
type LayerState struct {
	LastCompleteIndex int            `json:"last_complete_index"`
	SegmentHashes     map[int]string `json:"segment_hashes"`
	Status            string         `json:"status"`
	Failure           *fault.Error   `json:"failure,omitempty"`
}

// Checkpoint is the resumable job state. It is atomically replaced only after a
// complete segment boundary has been validated.
type Checkpoint struct {
	SchemaVersion int                    `json:"schema_version"`
	OutDir        string                 `json:"out_dir"`
	InputSHA256   string                 `json:"input_sha256"`
	ParamsSHA256  string                 `json:"params_sha256"`
	Platform      string                 `json:"platform"`
	Layers        map[string]*LayerState `json:"layers"`
	path          string
}

// NewCheckpoint creates an in-memory checkpoint for layers.
func NewCheckpoint(outDir, inputSHA, paramsSHA, platform string, layers []string) *Checkpoint {
	cp := &Checkpoint{
		SchemaVersion: 1,
		OutDir:        outDir,
		InputSHA256:   inputSHA,
		ParamsSHA256:  paramsSHA,
		Platform:      platform,
		Layers:        map[string]*LayerState{},
		path:          CheckpointPath(outDir),
	}
	for _, name := range layers {
		cp.Layers[name] = &LayerState{LastCompleteIndex: -1, SegmentHashes: map[int]string{}, Status: "new"}
	}
	return cp
}

// CheckpointPath returns the canonical checkpoint location.
func CheckpointPath(outDir string) string {
	return filepath.Join(outDir, "work", "checkpoint.json")
}

// Load reads a checkpoint.
func Load(path string) (*Checkpoint, error) {
	b, err := os.ReadFile(path)
	if err != nil {
		return nil, fault.Wrap(fault.Checkpoint, err, "read checkpoint")
	}
	var cp Checkpoint
	if err := json.Unmarshal(b, &cp); err != nil {
		return nil, fault.Wrap(fault.Checkpoint, err, "parse checkpoint")
	}
	cp.path = path
	if cp.Layers == nil {
		cp.Layers = map[string]*LayerState{}
	}
	for _, l := range cp.Layers {
		if l.SegmentHashes == nil {
			l.SegmentHashes = map[int]string{}
		}
	}
	return &cp, nil
}

// LoadOrNew loads or creates a checkpoint and verifies identity when a file
// exists. A digest mismatch is returned rather than silently restarting.
func LoadOrNew(outDir, inputSHA, paramsSHA, platform string, layers []string) (*Checkpoint, error) {
	path := CheckpointPath(outDir)
	cp, err := Load(path)
	if errors.Is(err, os.ErrNotExist) {
		return NewCheckpoint(outDir, inputSHA, paramsSHA, platform, layers), nil
	}
	if err != nil {
		return nil, err
	}
	if err := cp.VerifyIdentity(inputSHA, paramsSHA, platform); err != nil {
		return nil, err
	}
	// Add layers introduced by a compatible resume configuration.
	for _, name := range layers {
		if _, ok := cp.Layers[name]; !ok {
			cp.Layers[name] = &LayerState{LastCompleteIndex: -1, SegmentHashes: map[int]string{}, Status: "new"}
		}
	}
	return cp, nil
}

// VerifyIdentity rejects continuation when source, parameters, or platform do
// not exactly match the checkpoint.
func (cp *Checkpoint) VerifyIdentity(inputSHA, paramsSHA, platform string) error {
	if cp.InputSHA256 != inputSHA || cp.ParamsSHA256 != paramsSHA || cp.Platform != platform {
		return fault.New(fault.DigestMismatch,
			"checkpoint identity mismatch: checkpoint=(%s,%s,%s) current=(%s,%s,%s); previous release retained",
			cp.InputSHA256, cp.ParamsSHA256, cp.Platform, inputSHA, paramsSHA, platform)
	}
	return nil
}

// Save atomically persists the checkpoint.
func (cp *Checkpoint) Save() error {
	b, err := index.CanonicalJSON(cp)
	if err != nil {
		return err
	}
	if err := index.AtomicWriteFile(cp.path, b, 0o644); err != nil {
		return fault.Wrap(fault.Checkpoint, err, "save checkpoint")
	}
	return nil
}

// NextIndex returns the first segment index that must be built for a layer.
func (cp *Checkpoint) NextIndex(layer string) int {
	st := cp.Layers[layer]
	if st == nil {
		return 0
	}
	return st.LastCompleteIndex + 1
}

// CommitSegment commits one validated complete boundary.
func (cp *Checkpoint) CommitSegment(layer string, segmentIndex int, hash string) error {
	st, ok := cp.Layers[layer]
	if !ok {
		st = &LayerState{LastCompleteIndex: -1, SegmentHashes: map[int]string{}}
		cp.Layers[layer] = st
	}
	if segmentIndex != st.LastCompleteIndex+1 {
		return fault.InLayer(fault.Checkpoint, layer, segmentIndex+1, int64(segmentIndex),
			"non-contiguous checkpoint commit after index %d", st.LastCompleteIndex)
	}
	st.LastCompleteIndex = segmentIndex
	st.SegmentHashes[segmentIndex] = hash
	st.Status = "complete"
	return cp.Save()
}

// MarkFailed records a layer-only failure without advancing its boundary.
func (cp *Checkpoint) MarkFailed(layer string, fe *fault.Error) error {
	st, ok := cp.Layers[layer]
	if !ok {
		st = &LayerState{LastCompleteIndex: -1, SegmentHashes: map[int]string{}}
		cp.Layers[layer] = st
	}
	st.Status = "failed"
	st.Failure = fe
	return cp.Save()
}

// LayerNames returns sorted layer names.
func (cp *Checkpoint) LayerNames() []string {
	out := make([]string, 0, len(cp.Layers))
	for name := range cp.Layers {
		out = append(out, name)
	}
	sort.Strings(out)
	return out
}

// ReleasePaths groups current and previous published manifests.
type ReleasePaths struct {
	Dir      string `json:"dir"`
	Current  string `json:"current"`
	Previous string `json:"previous"`
}

// Publish atomically publishes manifest, moving any existing current manifest
// to previous so Rollback can restore it.
func Publish(outDir string, manifest []byte) (int, error) {
	relDir := filepath.Join(outDir, "published")
	versionDir := filepath.Join(relDir, "versions")
	if err := os.MkdirAll(versionDir, 0o755); err != nil {
		return 0, fault.Wrap(fault.Publish, err, "create release directory")
	}
	current := filepath.Join(relDir, "current.json")
	previous := filepath.Join(relDir, "previous.json")

	if old, err := os.ReadFile(current); err == nil {
		if err := index.AtomicWriteFile(previous, old, 0o644); err != nil {
			return 0, fault.Wrap(fault.Publish, err, "retain previous manifest")
		}
	} else if !errors.Is(err, os.ErrNotExist) {
		return 0, fault.Wrap(fault.Publish, err, "read current manifest")
	}

	version := nextVersion(versionDir)
	versionPath := filepath.Join(versionDir, "release-"+padVersion(version)+".json")
	if err := index.AtomicWriteFile(versionPath, manifest, 0o644); err != nil {
		return 0, fault.Wrap(fault.Publish, err, "write versioned manifest")
	}
	if err := index.AtomicWriteFile(current, manifest, 0o644); err != nil {
		return 0, fault.Wrap(fault.Publish, err, "replace current manifest")
	}
	return version, nil
}

// Rollback restores previous.json to current.json. The failed/newer manifest is
// preserved as previous.json, allowing the operator to inspect or toggle back.
func Rollback(outDir string) ([]byte, error) {
	relDir := filepath.Join(outDir, "published")
	current := filepath.Join(relDir, "current.json")
	previous := filepath.Join(relDir, "previous.json")
	prev, err := os.ReadFile(previous)
	if err != nil {
		return nil, fault.Wrap(fault.Publish, err, "read previous release for rollback")
	}
	cur, _ := os.ReadFile(current)
	if len(cur) > 0 {
		if err := index.AtomicWriteFile(previous, cur, 0o644); err != nil {
			return nil, fault.Wrap(fault.Publish, err, "preserve current during rollback")
		}
	}
	if err := index.AtomicWriteFile(current, prev, 0o644); err != nil {
		return nil, fault.Wrap(fault.Publish, err, "write rolled-back manifest")
	}
	return prev, nil
}

// NextVersion exposes the next zero-padded release version number.
func NextVersion(outDir string) int {
	return nextVersion(filepath.Join(outDir, "published", "versions"))
}

func nextVersion(dir string) int {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return 1
	}
	max := 0
	for _, e := range entries {
		name := strings.TrimPrefix(e.Name(), "release-")
		name = strings.TrimSuffix(name, ".json")
		if n, err := strconv.Atoi(name); err == nil && n > max {
			max = n
		}
	}
	return max + 1
}

func padVersion(v int) string {
	s := strconv.Itoa(v)
	for len(s) < 6 {
		s = "0" + s
	}
	return s
}
