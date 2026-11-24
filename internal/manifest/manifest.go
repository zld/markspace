// Package manifest writes the deterministic distribution manifest: per
// segment time bounds, target/actual bitrate, audio parameters and alignment.
package manifest

import (
	"encoding/json"
	"os"
	"path/filepath"

	"mediad/internal/mederr"
)

// AudioParams is the normalized audio configuration for a segment.
type AudioParams struct {
	TargetRate     int     `json:"target_sample_rate"`
	Channels       int     `json:"channels"`
	Layout         string  `json:"layout"`
	BitsPerSample  int     `json:"bits_per_sample"`
	LoudnessTarget float64 `json:"loudness_target_lufs"`
	PeakCeilingDB  float64 `json:"peak_ceiling_dbfs"`
}

// Segment is one EDL clip/transition placement in the manifest.
type Segment struct {
	Index               int         `json:"index"`
	Track               string      `json:"track"`
	StartNs             int64       `json:"start_ns"`
	EndNs               int64       `json:"end_ns"`
	StartMs             float64     `json:"start_ms"`
	EndMs               float64     `json:"end_ms"`
	DurationMs          float64     `json:"duration_ms"`
	TransitionInMs      float64     `json:"transition_in_ms"`
	TargetBitrate       int64       `json:"bitrate_target"`
	ActualBitrate       float64     `json:"bitrate_actual"`
	BitrateDeviationPct float64     `json:"bitrate_deviation_pct"`
	TimelineAlignmentMs float64     `json:"timeline_alignment_error_ms"`
	Audio               AudioParams `json:"audio"`
	OutputFile          string      `json:"output_file,omitempty"`
	SHA256              string      `json:"sha256,omitempty"`
}

// Chunk is the manifest's stable chunk reference.
type Chunk struct {
	Index   int    `json:"index"`
	StartNs int64  `json:"start_ns"`
	EndNs   int64  `json:"end_ns"`
	File    string `json:"file"`
	Bytes   int64  `json:"bytes"`
	SHA256  string `json:"sha256"`
}

// Report contains the acceptance summary fields printed by the CLI.
type Report struct {
	OK                       bool            `json:"ok"`
	BitrateTarget            int64           `json:"bitrate_target"`
	BitrateActual            float64         `json:"bitrate_actual"`
	BitrateDeviationPct      float64         `json:"bitrate_deviation_pct"`
	PeakDBFS                 float64         `json:"peak_dbfs"`
	IntegratedLUFS           float64         `json:"integrated_lufs"`
	TimelineAlignmentErrorMs float64         `json:"timeline_alignment_error_ms"`
	ChunksCommitted          int             `json:"chunks_committed"`
	ChunksNewlyCommitted     int             `json:"chunks_newly_committed,omitempty"`
	ChunksReused             int             `json:"chunks_reused"`
	OutputHash               string          `json:"output_hash,omitempty"`
	IndexRebuilt             bool            `json:"index_rebuilt,omitempty"`
	TruncatedChunksRedone    []int           `json:"truncated_chunks_redone,omitempty"`
	OverlapRejected          bool            `json:"overlap_rejected,omitempty"`
	Errors                   []*mederr.Error `json:"errors"`
}

// Manifest is written to manifest.json in the output directory. It is
// deterministic: no wall-clock timestamps or absolute paths are stored.
type Manifest struct {
	SchemaVersion int         `json:"schema_version"`
	Seed          int64       `json:"seed"`
	Fingerprint   string      `json:"parameter_fingerprint"`
	ChunkNs       int64       `json:"chunk_ns"`
	TotalStartNs  int64       `json:"total_start_ns"`
	TotalEndNs    int64       `json:"total_end_ns"`
	Report        Report      `json:"-"`
	Audio         AudioParams `json:"audio"`
	Segments      []Segment   `json:"segments"`
	Chunks        []Chunk     `json:"chunks"`
}

// WriteFile atomically writes indented JSON manifest.
func WriteFile(dir, name string, m *Manifest) (string, error) {
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return "", mederr.New(mederr.CatIO, "manifest", -1, -1, false, "create dir: %v", err)
	}
	data, err := json.MarshalIndent(m, "", "  ")
	if err != nil {
		return "", mederr.New(mederr.CatParse, "manifest", -1, -1, false, "marshal: %v", err)
	}
	data = append(data, '\n')
	path := filepath.Join(dir, name)
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, data, 0o644); err != nil {
		return "", mederr.New(mederr.CatIO, "manifest", -1, -1, false, "write temp: %v", err)
	}
	if err := os.Rename(tmp, path); err != nil {
		_ = os.Remove(tmp)
		return "", mederr.New(mederr.CatIO, "manifest", -1, -1, false, "commit: %v", err)
	}
	return path, nil
}
