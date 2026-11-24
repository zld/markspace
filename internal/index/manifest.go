package index

import "mediapack/internal/demux"

// TrackManifest is the per-output-track verification record.
type TrackManifest struct {
	TargetID         uint32   `json:"target_id"`
	Label            string   `json:"label,omitempty"`
	Source           string   `json:"source"`
	Kind             string   `json:"kind,omitempty"`
	Codec            string   `json:"codec,omitempty"`
	Timescale        uint32   `json:"timescale"`
	SampleCount      int      `json:"sample_count"`
	PayloadBytes     int64    `json:"payload_bytes"`
	PayloadHash      string   `json:"payload_hash"`
	SampleHashes     []string `json:"sample_hashes,omitempty"`
	ClipBoundaries   []any    `json:"clip_boundaries,omitempty"`
	TransitionRanges []any    `json:"transition_ranges,omitempty"`
	TargetBitrateBps float64  `json:"target_bitrate_bps,omitempty"`
	ActualBitrateBps float64  `json:"actual_bitrate_bps,omitempty"`
	DurationSeconds  float64  `json:"duration_seconds"`
	AlignmentErrorMS float64  `json:"alignment_error_ms,omitempty"`
}

// Manifest is the stable JSON deliverable. Verify prints a subset/aggregate.
type Manifest struct {
	Version                  int                `json:"version"`
	OutputMP4                string             `json:"output_mp4"`
	PayloadHash              string             `json:"payload_hash,omitempty"`
	Tracks                   []TrackManifest    `json:"tracks"`
	Transitions              []any              `json:"transition_ranges,omitempty"`
	SampleCount              int                `json:"samples"`
	PayloadHashMismatches    int                `json:"payload_hash_mismatches"`
	TargetBitrateBps         float64            `json:"target_bitrate_bps"`
	ActualBitrateBps         float64            `json:"actual_bitrate_bps"`
	BitrateDeviationPct      float64            `json:"bitrate_deviation_pct"`
	QualityDroppedSamples    int                `json:"quality_dropped_samples"`
	ManifestAlignmentErrorMS float64            `json:"manifest_alignment_error_ms"`
	RecoveredChunks          int                `json:"recovered_chunks"`
	CommittedChunks          int                `json:"committed_chunks"`
	ErrorRanges              []demux.ErrorRange `json:"error_ranges,omitempty"`
	Chunks                   []ChunkRecord      `json:"chunks,omitempty"`
}
