package provenance

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"

	"mediarun/internal/fault"
)

// WriteAtomic serializes the manifest and replaces path atomically. A crash
// mid-write leaves either the previous manifest or the new one, never a
// truncated file.
func WriteAtomic(path string, m *Manifest) error {
	if m == nil {
		return fault.New(fault.StagePipeline, fault.KindConfig, "nil manifest")
	}
	SortFaults(m.Faults)
	for i := range m.Stages {
		SortFaults(m.Stages[i].Faults)
	}
	for i := range m.Segments {
		SortFaults(m.Segments[i].Faults)
	}
	data, err := CanonicalJSON(m)
	if err != nil {
		return fault.Wrap(fault.StagePipeline, fault.KindIO, err, "marshal manifest")
	}
	return WriteFileAtomic(path, data)
}

// WriteFileAtomic writes data to a sibling temporary file, fsyncs it and
// renames it over path.
func WriteFileAtomic(path string, data []byte) error {
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return fault.Wrap(fault.StagePipeline, fault.KindIO, err, "create %s", dir)
	}
	tmp, err := os.CreateTemp(dir, ".mediarun-*.tmp")
	if err != nil {
		return fault.Wrap(fault.StagePipeline, fault.KindIO, err, "temp file in %s", dir)
	}
	name := tmp.Name()
	cleanup := func() {
		tmp.Close()
		os.Remove(name)
	}
	if _, err := tmp.Write(data); err != nil {
		cleanup()
		return fault.Wrap(fault.StagePipeline, fault.KindIO, err, "write %s", name)
	}
	if err := tmp.Sync(); err != nil {
		cleanup()
		return fault.Wrap(fault.StagePipeline, fault.KindIO, err, "fsync %s", name)
	}
	if err := tmp.Close(); err != nil {
		os.Remove(name)
		return fault.Wrap(fault.StagePipeline, fault.KindIO, err, "close %s", name)
	}
	if err := os.Rename(name, path); err != nil {
		os.Remove(name)
		return fault.Wrap(fault.StagePipeline, fault.KindIO, err, "rename %s", path)
	}
	return nil
}

// Load reads a manifest from disk.
func Load(path string) (*Manifest, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	var m Manifest
	if err := json.Unmarshal(data, &m); err != nil {
		return nil, fmt.Errorf("manifest %s: %w", path, err)
	}
	return &m, nil
}

// Validate performs structural checks used before trusting a checkpoint or a
// comparison baseline.
func (m *Manifest) Validate() error {
	if m == nil {
		return errors.New("nil manifest")
	}
	if m.Version != Version {
		return fmt.Errorf("manifest version %d, want %d", m.Version, Version)
	}
	if m.Timescale == 0 {
		return errors.New("manifest timescale is zero")
	}
	for i, s := range m.Segments {
		if s.Index != i {
			return fmt.Errorf("segment %d has index %d", i, s.Index)
		}
	}
	return nil
}

// SegmentHashes returns the per-segment digests in index order.
func (m *Manifest) SegmentHashes() []string {
	out := make([]string, len(m.Segments))
	for i, s := range m.Segments {
		out[i] = s.SHA256
	}
	return out
}

// EqualHashes compares two digest slices.
func EqualHashes(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

// Summarize renders the acceptance line for terminal output.
func (m *Manifest) Summarize() string {
	parseErrors := m.Metrics.ParseErrors
	first := int64(-1)
	for _, f := range m.Faults {
		if f.Offset >= 0 && (first < 0 || f.Offset < first) {
			first = f.Offset
		}
	}
	return fmt.Sprintf("frames=%d parse_errors=%d first_error_offset=%d recovered_audio=%d segments=%d",
		m.Metrics.Frames, parseErrors, first, m.Metrics.RecoveredAudio, m.Metrics.Segments)
}

// HashFile returns the digest and size of a file.
func HashFile(path string) (string, int64, error) {
	f, err := os.Open(path)
	if err != nil {
		return "", 0, err
	}
	defer f.Close()
	h := sha256.New()
	n, err := io.Copy(h, f)
	if err != nil {
		return "", 0, err
	}
	return hex.EncodeToString(h.Sum(nil)), n, nil
}

// CanonicalJSON marshals v deterministically. encoding/json emits struct
// fields in declaration order and sorts map keys, so no extra sorting is
// needed; this helper exists to centralize the choice and the trailing
// newline convention.
func CanonicalJSON(v any) ([]byte, error) {
	b, err := json.MarshalIndent(v, "", "  ")
	if err != nil {
		return nil, err
	}
	return append(b, '\n'), nil
}
