package report

import (
	"io"

	"mediapack/internal/index"
)

// SegmentHash identifies one generated fragment.
type SegmentHash struct {
	Layer  string `json:"layer"`
	Index  int    `json:"index"`
	SHA256 string `json:"sha256"`
}

// LayerStatus is the per-representation state in a release manifest.
type LayerStatus struct {
	Name              string         `json:"name"`
	Status            string         `json:"status"`
	LastCompleteIndex int            `json:"last_complete_index"`
	SegmentCount      int            `json:"segment_count"`
	SegmentHashes     []SegmentHash  `json:"segment_hashes"`
	Error             *FailureReport `json:"error,omitempty"`
}

// FailureReport is the stable terminal error JSON.
type FailureReport struct {
	Kind     string  `json:"kind"`
	Message  string  `json:"message"`
	Layer    string  `json:"layer,omitempty"`
	Segment  *int    `json:"segment,omitempty"`
	Offset   int64   `json:"offset,omitempty"`
	OffsetMS float64 `json:"offset_ms,omitempty"`
	Source   string  `json:"source,omitempty"`
	ExitCode int     `json:"exit_code"`
}

// Report is the acceptance JSON object and release manifest body.
type Report struct {
	OK              bool             `json:"ok"`
	Command         string           `json:"command"`
	Out             string           `json:"out"`
	Platform        string           `json:"platform"`
	InputSHA256     string           `json:"input_sha256"`
	ManifestSHA256  string           `json:"manifest_sha256,omitempty"`
	ReusedAssets    int              `json:"reused_assets"`
	RebuiltAssets   int              `json:"rebuilt_assets"`
	ReusedAssetIDs  []string         `json:"reused_asset_ids"`
	RebuiltAssetIDs []string         `json:"rebuilt_asset_ids"`
	AssetDecisions  []index.Decision `json:"asset_decisions,omitempty"`

	// SegmentSHA256 is a stable aggregate of all ordered fragment hashes.
	SegmentSHA256     string            `json:"segment_sha256"`
	SegmentHashes     []string          `json:"segment_hashes"`
	SegmentSHA256ByID map[string]string `json:"segment_sha256_by_id"`

	PixelMean        float64 `json:"pixel_mean"`
	PeakDBFS         float64 `json:"peak_dbfs"`
	AlignmentErrorMS float64 `json:"alignment_error_ms"`
	FrameDurationMS  float64 `json:"frame_duration_ms"`
	MaxTickError     int64   `json:"max_tick_error"`

	TimeRange       TimeRange         `json:"time_range"`
	Layers          []LayerStatus     `json:"layers"`
	SubtitleOutputs map[string]string `json:"subtitle_outputs,omitempty"`
	MP4Outputs      map[string]string `json:"mp4_outputs,omitempty"`
	Version         int               `json:"version,omitempty"`
	Published       bool              `json:"published"`
	Failure         *FailureReport    `json:"failure,omitempty"`
}

// TimeRange records shared-clock coverage.
type TimeRange struct {
	StartTicks int64   `json:"start_ticks"`
	EndTicks   int64   `json:"end_ticks"`
	StartMS    float64 `json:"start_ms"`
	EndMS      float64 `json:"end_ms"`
}

// WriteJSON writes indented deterministic JSON to w.
func WriteJSON(w io.Writer, v any) error {
	b, err := index.CanonicalJSON(v)
	if err != nil {
		return err
	}
	_, err = w.Write(b)
	return err
}

// CanonicalBytes returns deterministic JSON bytes.
func CanonicalBytes(v any) ([]byte, error) {
	return index.CanonicalJSON(v)
}
