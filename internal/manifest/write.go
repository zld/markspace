// Package manifest writes deterministic per-segment media manifests.
package manifest

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"os"
	"path/filepath"

	"mediacheck/internal/gap"
	"mediacheck/internal/timebase"
)

const SchemaVersion = "media-check/v1"

// Segment is one derived fragment's complete accounting record.
type Segment struct {
	Index          int       `json:"index"`
	TrackID        string    `json:"track_id"`
	File           string    `json:"file"`
	FragmentSHA256 string    `json:"fragment_sha256"`
	StartSample    int64     `json:"start_sample"`
	EndSample      int64     `json:"end_sample"`
	StartTick      int64     `json:"start_tick"`
	EndTick        int64     `json:"end_tick"`
	SourceOffset   int64     `json:"source_offset"`
	SourceLength   int64     `json:"source_length_bytes"`
	OutputBits     int       `json:"output_bits"`
	Channels       int       `json:"channels"`
	SampleRate     int       `json:"sample_rate"`
	TargetBPS      int64     `json:"target_bps"`
	ActualBPS      float64   `json:"actual_bps"`
	DeltaPct       float64   `json:"delta_pct"`
	PayloadBytes   int64     `json:"payload_bytes"`
	ContainerBytes int64     `json:"container_bytes"`
	Delivered      bool      `json:"delivered"`
	Reason         string    `json:"reason,omitempty"`
	GapCount       int       `json:"gap_count"`
	Gaps           []gap.Gap `json:"gaps"`
}

// Quality contains source comparison and alignment totals.
type Quality struct {
	TargetBPS       int64   `json:"target_bps"`
	ActualBPS       float64 `json:"actual_bps"`
	DeltaPct        float64 `json:"delta_pct"`
	SNRDB           float64 `json:"snr_db"`
	AlignErrSamples int     `json:"align_err_samples"`
	BadOffset       int64   `json:"bad_offset"`
	GapCount        int     `json:"gap_count"`
}

// Manifest is the stable, timestamp-free description of a track delivery.
type Manifest struct {
	SchemaVersion string    `json:"schema_version"`
	Case          string    `json:"case,omitempty"`
	TrackID       string    `json:"track_id"`
	SourceSHA256  string    `json:"source_sha"`
	ConfigSHA256  string    `json:"config_sha"`
	SampleRate    int       `json:"sample_rate"`
	Channels      int       `json:"channels"`
	Segments      []Segment `json:"segments"`
	Gaps          []gap.Gap `json:"gaps"`
	Quality       Quality   `json:"quality"`
}

// Encode returns deterministic indented JSON.
func (m *Manifest) Encode() ([]byte, error) {
	if m == nil {
		return nil, timebase.NewError(timebase.CodeManifest, "nil manifest")
	}
	if m.SchemaVersion == "" {
		m.SchemaVersion = SchemaVersion
	}
	b, err := json.MarshalIndent(m, "", "  ")
	if err != nil {
		return nil, timebase.NewError(timebase.CodeManifest, "encode manifest: %v", err).Wrap(err)
	}
	return append(b, '\n'), nil
}

func DigestBytes(b []byte) string {
	sum := sha256.Sum256(b)
	return hex.EncodeToString(sum[:])
}

// Digest encodes and hashes the manifest. The encoding has no wall-clock fields.
func (m *Manifest) Digest() (string, []byte, error) {
	b, err := m.Encode()
	if err != nil {
		return "", nil, err
	}
	return DigestBytes(b), b, nil
}

// WriteFile atomically writes deterministic manifest JSON.
func (m *Manifest) WriteFile(path string) (string, error) {
	b, err := m.Encode()
	if err != nil {
		return "", err
	}
	if err := AtomicWriteFile(path, b, 0o644); err != nil {
		return "", err
	}
	return DigestBytes(b), nil
}

// AtomicWriteFile writes in the destination directory, fsyncs the file, and
// renames it, so readers never observe a half-written derivative.
func AtomicWriteFile(path string, data []byte, mode os.FileMode) error {
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return timebase.NewError(timebase.CodeManifest, "create output directory").Wrap(err)
	}
	tmp, err := os.CreateTemp(dir, ".tmp-manifest-*")
	if err != nil {
		return timebase.NewError(timebase.CodeManifest, "create temporary manifest").Wrap(err)
	}
	tmpName := tmp.Name()
	cleanup := true
	defer func() {
		if cleanup {
			_ = os.Remove(tmpName)
		}
	}()
	if _, err := tmp.Write(data); err != nil {
		_ = tmp.Close()
		return timebase.NewError(timebase.CodeManifest, "write temporary manifest").Wrap(err)
	}
	if err := tmp.Chmod(mode); err != nil {
		_ = tmp.Close()
		return timebase.NewError(timebase.CodeManifest, "chmod manifest").Wrap(err)
	}
	if err := tmp.Sync(); err != nil {
		_ = tmp.Close()
		return timebase.NewError(timebase.CodeManifest, "sync manifest").Wrap(err)
	}
	if err := tmp.Close(); err != nil {
		return timebase.NewError(timebase.CodeManifest, "close manifest").Wrap(err)
	}
	if err := os.Rename(tmpName, path); err != nil {
		return timebase.NewError(timebase.CodeManifest, "atomic rename manifest").Wrap(err)
	}
	cleanup = false
	return nil
}
