// Package pipeline chains indexing, timeline synthesis, audio/colour
// normalisation, offscreen frame export, RTP recovery and MP4 remuxing into
// one cancellable streaming run with a single shared clock and error model.
package pipeline

import (
	"context"
	"io"

	"mediacheck/audio"
	"mediacheck/color"
	"mediacheck/internal/fixture"
	"mediacheck/rtp"
)

// ReadSeekerAt is the seekable stream contract the pipeline consumes.
type ReadSeekerAt interface {
	io.Reader
	io.ReaderAt
	io.Seeker
}

// Config parametrises one end-to-end run.
type Config struct {
	IndexDir         string
	OutputPath       string // final MP4 path; empty skips remuxing
	Frames           int    // fixture frame count and render sample count
	Width, Height    int
	FPS              int
	TargetSampleRate int64
	SyncLimitMs      int64 // publish threshold for AV sync error
	P95LimitMs       float64

	ColorPolicy color.Policy
	AudioPolicy audio.Config

	RTPWindow       int64
	RTPLossRate     float64
	RTPJitterMs     int
	RTPHardFraction float64
	RTPClockRate    int64
}

// Defaults fills standard broadcast test limits.
func (c *Config) Defaults() {
	if c.Frames == 0 {
		c.Frames = 64
	}
	if c.FPS == 0 {
		c.FPS = 24
	}
	if c.TargetSampleRate == 0 {
		c.TargetSampleRate = 48000
	}
	if c.SyncLimitMs == 0 {
		c.SyncLimitMs = 20
	}
	if c.P95LimitMs == 0 {
		c.P95LimitMs = 50
	}
	if c.RTPWindow == 0 {
		c.RTPWindow = rtp.DefaultWindow
	}
	if c.RTPClockRate == 0 {
		c.RTPClockRate = 90000
	}
}

// FeedConfig derives the deterministic RTP return-feed simulator settings.
func (c *Config) FeedConfig(seed int64) fixture.FeedConfig {
	return fixture.FeedConfig{
		Seed:             seed,
		Packets:          c.Frames,
		LossRate:         c.RTPLossRate,
		JitterMs:         c.RTPJitterMs,
		Window:           int(c.RTPWindow),
		DuplicateRate:    0.02,
		ClockRate:        c.RTPClockRate,
		HardLossFraction: c.RTPHardFraction,
	}
}

// Metrics is the reviewer-facing, deterministic run report.
type Metrics struct {
	InputSHA256        string   `json:"input_sha256"`
	Frames             int      `json:"frames"`
	AudioSamplesIn     int64    `json:"audio_samples_in"`
	AudioSamplesOut    int64    `json:"audio_samples_out"`
	AudioDurationErrNs int64    `json:"audio_duration_error_ns"`
	AVSyncErrorMs      int64    `json:"av_sync_error_ms"`
	RTPLost            int      `json:"rtp_lost"`
	RTPRecovered       int      `json:"rtp_recovered"`
	RTPUnrecoverable   int      `json:"rtp_unrecoverable"`
	RecoveryRate       float64  `json:"recovery_rate"`
	P95LatencyMs       float64  `json:"p95_latency_ms"`
	IndexRebuilt       bool     `json:"index_rebuilt"`
	FrameHashes        []string `json:"frame_hashes"`

	// Integrity / provenance fields.
	TruncatedAt       int64    `json:"truncated_at,omitempty"`
	CRCFailures       []int64  `json:"crc_failures,omitempty"`
	TimelineTrusted   bool     `json:"timeline_trusted"`
	ColorSource       string   `json:"color_source,omitempty"`
	ColorUnsafeReason string   `json:"color_unsafe_reason,omitempty"`
	Published         bool     `json:"published"`
	CompletionMarker  string   `json:"completion_marker,omitempty"`
	Faults            []string `json:"faults,omitempty"`
}

// Run executes the full pipeline. A non-nil error is returned only for
// parameter/runtime failures (exit 1); corrupt-input conditions are recorded
// in Metrics.Faults and a nil error is returned so the caller maps exit 2.
type Runner struct {
	cfg Config
	ctx context.Context
}
