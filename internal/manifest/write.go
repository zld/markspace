// Package manifest writes the deterministic delivery manifest. The same
// inputs always produce byte-identical manifests: events stay sorted,
// maps are replaced by ordered slices and no wall-clock timestamps are
// emitted.
package manifest

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"os"
	"path/filepath"
)

// AnomalyRecord describes one untrusted timeline region in the manifest.
type AnomalyRecord struct {
	Kind       string `json:"kind"`
	TrackID    uint32 `json:"track_id"`
	StartTick  int64  `json:"start_tick"`
	EndTick    int64  `json:"end_tick"`
	StartIndex uint32 `json:"start_sample"`
	EndIndex   uint32 `json:"end_sample"`
	Detail     string `json:"detail,omitempty"`
}

// EventRecord is one event line with its verdict and provenance.
type EventRecord struct {
	ID          string             `json:"id"`
	Decision    string             `json:"decision"`
	MediaTick   int64              `json:"media_tick"`
	Timescale   uint32             `json:"timescale"`
	Confidence  float64            `json:"confidence"`
	RejectWhy   string             `json:"reject_reason,omitempty"`
	DuplicateOf string             `json:"duplicate_of,omitempty"`
	Clip        *ClipRecord        `json:"clip,omitempty"`
	Anomaly     *AnomalyRecord     `json:"anomaly,omitempty"`
}

// ClipRecord binds a delivered file to its source samples and digest.
type ClipRecord struct {
	FileName       string         `json:"file_name"`
	SHA256         string         `json:"sha256"`
	Size           int64          `json:"size"`
	StartPTS       int64          `json:"start_pts"`
	EndPTS         int64          `json:"end_pts"`
	DurationTicks  int64          `json:"duration_ticks"`
	Timescale      uint32         `json:"timescale"`
	VideoFirst     uint32         `json:"video_first_sample"`
	VideoLast      uint32         `json:"video_last_sample"`
	SampleOffsets  []SampleRecord `json:"sample_offsets"`
}

// SampleRecord is one source-to-output sample mapping.
type SampleRecord struct {
	TrackID      uint32 `json:"track_id"`
	OutputIndex  uint32 `json:"output_index"`
	SourceIndex  uint32 `json:"source_index"`
	SourceOffset int64  `json:"source_offset"`
	Size         uint32 `json:"size"`
}

// Stats mirrors the terminal counters inside the manifest.
type Stats struct {
	Accepted       int     `json:"accepted"`
	Duplicate      int     `json:"duplicate"`
	SkipBudget     int     `json:"skip_budget"`
	SkipConfidence int     `json:"skip_confidence"`
	SkipUntrusted  int     `json:"skip_untrusted"`
	NoKeyframe     int     `json:"no_keyframe"`
	RTPBad         int     `json:"rtp_bad"`
	RTPWraps       int     `json:"rtp_wraps"`
	AnomalyCount   int     `json:"anomaly_intervals"`
	Affected       int     `json:"affected"`
	ClipS          float64 `json:"clip_s"`
	BudgetS        float64 `json:"budget_s"`
	DeviationS     float64 `json:"deviation_s"`
	// Cache hit/rebuild counters are intentionally excluded: they describe
	// how a run was executed, not what was delivered, so repeat runs with
	// identical artifacts must produce identical manifests.
}

// Manifest is the complete, deterministically ordered delivery document.
type Manifest struct {
	SchemaVersion int            `json:"schema_version"`
	Source        SourceRecord   `json:"source"`
	Events        []EventRecord  `json:"events"`
	Anomalies     []AnomalyRecord `json:"anomalies"`
	Stats         Stats          `json:"stats"`
	// SHA is the digest of the manifest with this field empty; it lets
	// repeat runs prove that manifests are bit-for-bit identical.
	SHA string `json:"manifest_sha256"`
}

// SourceRecord binds inputs and configuration by digest.
type SourceRecord struct {
	InputPath   string `json:"input_path"`
	InputSHA    string `json:"input_sha256"`
	InputSize   int64  `json:"input_size"`
	EventsSHA   string `json:"events_sha256"`
	ConfigSHA   string `json:"config_sha256"`
}

// Write serializes the manifest atomically and fills its self-digest.
func Write(path string, m *Manifest) error {
	m.SHA = ""
	raw, err := json.MarshalIndent(m, "", "  ")
	if err != nil {
		return err
	}
	sum := sha256.Sum256(raw)
	m.SHA = hex.EncodeToString(sum[:])
	raw, err = json.MarshalIndent(m, "", "  ")
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	tmp, err := os.CreateTemp(filepath.Dir(path), ".mf-*")
	if err != nil {
		return err
	}
	name := tmp.Name()
	if _, err := tmp.Write(raw); err != nil {
		tmp.Close()
		os.Remove(name)
		return err
	}
	if err := tmp.Close(); err != nil {
		os.Remove(name)
		return err
	}
	return os.Rename(name, path)
}

// Digest computes the self-digest without writing anything, used by tests
// to compare repeat-run manifests.
func Digest(m *Manifest) (string, error) {
	cp := *m
	cp.SHA = ""
	raw, err := json.MarshalIndent(&cp, "", "  ")
	if err != nil {
		return "", err
	}
	sum := sha256.Sum256(raw)
	return hex.EncodeToString(sum[:]), nil
}
