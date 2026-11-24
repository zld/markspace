package metrics

import (
	"mediad/internal/index"
	"mediad/internal/mp4"
)

// CameraMetric is one delivered angle.
type CameraMetric struct {
	Camera string `json:"camera"`
	// WindowStartNs/WindowEndNs are the manifest's editorial interval.
	WindowStartNs int64 `json:"window_start_ns"`
	WindowEndNs   int64 `json:"window_end_ns"`
	// Delivered first/last presentation inside the window.
	FirstNs          int64 `json:"first_ns"`
	LastNs           int64 `json:"last_ns"`
	Samples          int   `json:"samples"`
	Bytes            int64 `json:"bytes"`
	Keyframes        int   `json:"keyframes"`
	TimestampRepairs int   `json:"timestamp_repairs"`
	// RTPWrapExtensions counts 32-bit sender-clock wraps that were extended.
	RTPWrapExtensions int   `json:"rtp_wrap_extensions,omitempty"`
	AlignmentErrorNs  int64 `json:"alignment_error_ns"`
	// Index range and largest seek interval for this camera.
	IndexRangeStartNs int64 `json:"index_range_start_ns"`
	IndexRangeEndNs   int64 `json:"index_range_end_ns"`
	MaxIntervalNs     int64 `json:"max_index_interval_ns"`
	// MessageBytes is the number of message bytes per sample used for PSNR.
	PSNRDb float64 `json:"psnr_db"`
}

// Report is the aggregate demo report.
type Report struct {
	TargetBps int64 `json:"target_bps"`
	ActualBps int64 `json:"actual_bps"`
	// DeliveryBps equals ActualBps (the delivered editorial window).
	DeliveryBps int64 `json:"delivery_bps"`
	// SourceBps is the throughput of the whole presented recording, including
	// material outside the editorial window.
	SourceBps        int64   `json:"source_bps"`
	BitrateDeltaPct  float64 `json:"bitrate_delta_pct"`
	Samples          int     `json:"samples"`
	Keyframes        int     `json:"keyframes"`
	TimestampRepairs int     `json:"timestamp_repairs"`
	// RTPWrapExtensions counts sender-clock wraps extended during repair.
	RTPWrapExtensions int     `json:"rtp_wrap_extensions"`
	IndexCoveragePct  float64 `json:"index_coverage_pct"`
	QualityPSNRDb     float64 `json:"quality_psnr_db"`
	AlignmentErrorMs  float64 `json:"alignment_error_ms"`
	// MaxIndexIntervalMs is the largest keyframe index gap across cameras and
	// SparseKeyframes reports whether any gap exceeds 10 s.
	MaxIndexIntervalMs int64          `json:"max_index_interval_ms"`
	SparseKeyframes    bool           `json:"sparse_keyframes"`
	Cameras            []CameraMetric `json:"cameras,omitempty"`
}

// Window describes the manifest interval for one camera, expressed in the same
// frame-relative nanoseconds as repaired sample NS (manifest offsets minus the
// camera's recording start).
type Window struct {
	Camera  string
	StartNs int64
	EndNs   int64
}

// Input bundles the repaired tracks and indexes for measurement.
type Input struct {
	TargetBps int64
	Windows   []Window
	Tracks    map[string]*mp4.RepairedTrack // camera -> primary video track
	Indexes   map[string]*index.TrackIndex
}

// Compute builds the report.
