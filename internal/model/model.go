// Package model defines the data structures shared across the bitrack pipeline:
// segment descriptors, encoding attempts, checkpoints and report records.
package model

import "time"

// SegmentState is the per-segment task state. See internal/task for legal moves.
type SegmentState string

const (
	StateQueued     SegmentState = "queued"
	StateRunning    SegmentState = "running"
	StateValidating SegmentState = "validating"
	StateCommitted  SegmentState = "committed"
	StateRetryable  SegmentState = "retryable"
	StateFailed     SegmentState = "failed"
	StatePaused     SegmentState = "paused"
)

// Segment describes one frame-aligned chunk of the source video.
type Segment struct {
	Index        int          `json:"index"`
	StartFrame   int          `json:"start_frame"`
	FrameCount   int          `json:"frame_count"`
	TargetBits   int64        `json:"target_bits"`
	ActualBits   int64        `json:"actual_bits"`
	Complexity   float64      `json:"complexity"`
	PSNRDB       float64      `json:"psnr_db"`
	BaselinePSNR float64      `json:"baseline_psnr_db"`
	DeltaDB      float64      `json:"delta_db"`
	State        SegmentState `json:"state"`
	OutputPath   string       `json:"output_path"`
	Checksum     string       `json:"checksum"`
	Attempts     []Attempt    `json:"attempts"`
	LastError    string       `json:"last_error,omitempty"`
}

// Attempt records one closed-loop encode trial of a segment.
type Attempt struct {
	Number     int     `json:"number"`
	TargetKbps int     `json:"target_kbps"`
	ActualBits int64   `json:"actual_bits"`
	Deviation  float64 `json:"deviation_pct"`
	Reason     string  `json:"reason"`
	OutputPath string  `json:"output_path"`
}

// Checkpoint is the atomically persisted pipeline state.
type Checkpoint struct {
	Version       int        `json:"version"`
	JobID         string     `json:"job_id"`
	InputPath     string     `json:"input_path"`
	InputSHA256   string     `json:"input_sha256"`
	ParamsDigest  string     `json:"params_digest"`
	Probe         ProbeInfo  `json:"probe"`
	Params        Params     `json:"params"`
	Segments      []*Segment `json:"segments"`
	CreatedAt     time.Time  `json:"created_at"`
	UpdatedAt     time.Time  `json:"updated_at"`
	InterruptKind string     `json:"interrupt_kind,omitempty"`
}

// ProbeInfo captures the validated properties of the source video.
type ProbeInfo struct {
	Width        int     `json:"width"`
	Height       int     `json:"height"`
	FrameRate    float64 `json:"frame_rate"`
	NbFrames     int     `json:"nb_frames"`
	DurationSec  float64 `json:"duration_sec"`
	PixelFormat  string  `json:"pixel_format"`
	FieldOrder   string  `json:"field_order"`
	IsCFR        bool    `json:"is_cfr"`
	HasVideo     bool    `json:"has_video"`
}

// Params is the full encoding parameter set; its digest gates checkpoint reuse.
type Params struct {
	TargetKbps       int     `json:"target_kbps"`
	SegmentFrames    int     `json:"segment_frames"`
	Width            int     `json:"width"`
	Height           int     `json:"height"`
	FrameRate        float64 `json:"frame_rate"`
	MaxIterations    int     `json:"iterations"`
	TolerancePct     float64 `json:"tolerance_pct"`
	MinSegmentKbps   int     `json:"min_segment_kbps"`
	Encoder          string  `json:"encoder"`
	Preset           string  `json:"preset"`
	PixelFormat      string  `json:"pixel_format"`
	MaxPSNRDropDB    float64 `json:"max_psnr_drop_db"`
	BaselineDigest   string  `json:"baseline_digest,omitempty"`
	FailureInjection string  `json:"failure_injection,omitempty"`
}

// SegmentReport is the per-segment view rendered in the JSON summary.
type SegmentReport struct {
	Index        int          `json:"index"`
	StartFrame   int          `json:"start_frame"`
	FrameCount   int          `json:"frame_count"`
	TargetBits   int64        `json:"target_bits"`
	ActualBits   int64        `json:"actual_bits"`
	PSNRDB       float64      `json:"psnr_db"`
	BaselinePSNR float64      `json:"baseline_psnr_db"`
	DeltaDB      float64      `json:"delta_db"`
	State        SegmentState `json:"state"`
	Attempts     []Attempt    `json:"attempts"`
	Reused       bool         `json:"reused"`
}

// Summary is the top-level JSON report emitted by run / resume / baseline.
type Summary struct {
	JobID              string          `json:"job_id"`
	Mode               string          `json:"mode"`
	TargetKbps         int             `json:"target_kbps"`
	ActualKbps         float64         `json:"actual_kbps"`
	BitrateDeviation   float64         `json:"bitrate_deviation_pct"`
	PSNRMeanDB         float64         `json:"psnr_mean_db"`
	SegmentPSNRStdDev  float64         `json:"segment_psnr_stddev_db"`
	RegressionCount    int             `json:"regression_count"`
	MaxPSNRDropDB      float64         `json:"max_psnr_drop_db"`
	ReusedSegments     int             `json:"reused_segments"`
	TotalFrames        int             `json:"total_frames"`
	DurationSec        float64         `json:"duration_sec"`
	Segments           []SegmentReport `json:"segments"`
	InputSHA256        string          `json:"input_sha256"`
	ParamsDigest       string          `json:"params_digest"`
	InterruptKind      string          `json:"interrupt_kind,omitempty"`
	Message            string          `json:"message,omitempty"`
}

// BaselineRecord binds quality numbers to the input fingerprint and parameters.
type BaselineRecord struct {
	InputSHA256  string          `json:"input_sha256"`
	ParamsDigest string          `json:"params_digest"`
	TargetKbps   int             `json:"target_kbps"`
	Probe        ProbeInfo       `json:"probe"`
	Segments     []SegmentReport `json:"segments"`
	PSNRMeanDB   float64         `json:"psnr_mean_db"`
	CreatedAt    time.Time       `json:"created_at"`
}
