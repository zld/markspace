// Package manifest writes and reads delivery manifests. A manifest is the
// auditable record of one sliced clip: window, timeline health, media
// policies applied, output identity and a self hash.
package manifest

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"

	"replay/internal/model"
)

// Version is the manifest schema version.
const Version = 1

// Manifest describes one produced clip.
type Manifest struct {
	Version     int            `json:"version"`
	Set         string         `json:"set"`
	Event       string         `json:"event"`
	Window      Window         `json:"window"`
	Timeline    TimelineInfo   `json:"timeline"`
	Audio       AudioInfo      `json:"audio"`
	Video       VideoInfo      `json:"video"`
	Output      Output         `json:"output"`
	Params      Params         `json:"params"`
	IssueCounts map[string]int `json:"issue_counts"`
	Hash        string         `json:"manifest_hash"`
}

// Window is the slice window in milliseconds on the media time base.
type Window struct {
	StartMS int64 `json:"start_ms"`
	EndMS   int64 `json:"end_ms"`
}

// SegmentInfo identifies one fragment used for the cut.
type SegmentInfo struct {
	Path    string `json:"path"`
	Seq     uint32 `json:"seq"`
	StartMS int64  `json:"start_ms"`
	DurMS   int64  `json:"dur_ms"`
}

// TimelineInfo summarizes timeline health and the fragments used.
type TimelineInfo struct {
	Fragments       int           `json:"fragments"`
	SeqGaps         int           `json:"seq_gaps"`
	Duplicates      int           `json:"duplicates"`
	TfdtRegressions int           `json:"tfdt_regressions"`
	MissingMS       [][2]int64    `json:"missing_intervals_ms"`
	Segments        []SegmentInfo `json:"segments"`
}

// AudioInfo records the audio normalization applied.
type AudioInfo struct {
	InputChannels    int    `json:"input_channels"`
	InputSampleRate  uint32 `json:"input_sample_rate"`
	OutputChannels   int    `json:"output_channels"`
	OutputSampleRate uint32 `json:"output_sample_rate"`
	Layout           string `json:"layout"`
	Resampled        bool   `json:"resampled"`
}

// VideoInfo records the color decision applied.
type VideoInfo struct {
	ColorSource *model.ColorInfo `json:"color_source"` // null when input metadata was missing
	ColorTarget model.ColorInfo  `json:"color_target"`
	Converted   bool             `json:"converted"`
	Note        string           `json:"note"`
}

// Output identifies the produced clip.
type Output struct {
	Path       string `json:"path"`
	SHA256     string `json:"sha256"`
	SizeBytes  int64  `json:"size_bytes"`
	DurationMS int64  `json:"duration_ms"`
	Bitrate    int64  `json:"bitrate"`
}

// Params records the parameters the clip was produced with.
type Params struct {
	WindowBeforeMS int64  `json:"window_before_ms"`
	WindowAfterMS  int64  `json:"window_after_ms"`
	TargetBitrate  int64  `json:"target_bitrate"`
	Workers        int    `json:"workers"`
	ParamsHash     string `json:"params_hash"`
}

// ComputeHash returns the sha256 of the canonical manifest encoding
// (Hash field cleared). Struct field order and encoding/json's sorted map
// keys make the encoding deterministic.
func ComputeHash(m *Manifest) (string, error) {
	c := *m
	c.Hash = ""
	b, err := json.Marshal(c)
	if err != nil {
		return "", err
	}
	sum := sha256.Sum256(b)
	return hex.EncodeToString(sum[:]), nil
}

// Write sets the manifest hash and writes the file atomically.
func Write(path string, m *Manifest) error {
	h, err := ComputeHash(m)
	if err != nil {
		return err
	}
	m.Hash = h
	b, err := json.MarshalIndent(m, "", "  ")
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, b, 0o644); err != nil {
		return err
	}
	return os.Rename(tmp, path)
}

// Load reads a manifest file.
func Load(path string) (*Manifest, error) {
	b, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	var m Manifest
	if err := json.Unmarshal(b, &m); err != nil {
		return nil, fmt.Errorf("manifest: %s: %w", path, err)
	}
	if m.Version != Version {
		return nil, fmt.Errorf("manifest: %s: unsupported version %d", path, m.Version)
	}
	return &m, nil
}
