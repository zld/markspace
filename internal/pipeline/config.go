package pipeline

import (
	"fmt"

	"mediarun/internal/clock"
	"mediarun/internal/fault"
	"mediarun/internal/frames"
	"mediarun/internal/provenance"
	"mediarun/internal/recovery"
)

// Config is the complete, hashable description of a pipeline run. Any field
// that can change output bytes or fault decisions belongs here; changing one
// invalidates the stages that depend on it.
type Config struct {
	Timescale       uint32 `json:"timescale"`
	AudioClockRate  uint32 `json:"audio_clock_rate"`
	AudioPayloadTyp uint8  `json:"audio_payload_type"`
	AudioSSRC       uint32 `json:"audio_ssrc"`
	JitterWindow    uint64 `json:"jitter_window"`
	JitterMax       int    `json:"jitter_max_packets"`
	MaxGapPackets   int    `json:"max_gap_packets"`
	ToleranceTicks  int64  `json:"tolerance_ticks"`
	SyncOffsetTicks int64  `json:"sync_offset_ticks"`
	SegmentCount    int    `json:"segment_count"`
	Width           int    `json:"width"`
	Height          int    `json:"height"`
	KeyframeEvery   int    `json:"keyframe_every"`
	MaxRecords      int    `json:"max_records"`
	MaxFrameBytes   int    `json:"max_frame_bytes"`
	FrameRateNum    int64  `json:"frame_rate_num"`
	FrameRateDen    int64  `json:"frame_rate_den"`
}

// DefaultConfig returns the acceptance configuration.
func DefaultConfig() Config {
	return Config{
		Timescale:       90000,
		AudioClockRate:  48000,
		AudioPayloadTyp: 111,
		AudioSSRC:       0x0BADCAFE,
		JitterWindow:    8,
		JitterMax:       32,
		MaxGapPackets:   3,
		ToleranceTicks:  900,
		SegmentCount:    4,
		Width:           1280,
		Height:          720,
		KeyframeEvery:   10,
		MaxRecords:      100000,
		MaxFrameBytes:   frames.MaxPayload,
		FrameRateNum:    25,
		FrameRateDen:    1,
	}
}

// Validate checks bounds that protect memory and arithmetic.
func (c Config) Validate() error {
	switch {
	case c.Timescale == 0 || c.Timescale > 1_000_000:
		return fmt.Errorf("timescale %d outside 1..1000000", c.Timescale)
	case c.AudioClockRate == 0 || c.AudioClockRate > 1_000_000:
		return fmt.Errorf("audio clock rate %d outside 1..1000000", c.AudioClockRate)
	case c.JitterWindow == 0 || c.JitterWindow > 4096:
		return fmt.Errorf("jitter window %d outside 1..4096", c.JitterWindow)
	case c.JitterMax <= 0 || c.JitterMax > 1<<20:
		return fmt.Errorf("jitter max packets %d outside 1..1048576", c.JitterMax)
	case c.MaxGapPackets < 0 || c.MaxGapPackets > 1000:
		return fmt.Errorf("max gap %d outside 0..1000", c.MaxGapPackets)
	case c.ToleranceTicks < 0:
		return fmt.Errorf("tolerance must be non-negative")
	case c.SegmentCount < 1 || c.SegmentCount > 256:
		return fmt.Errorf("segment count %d outside 1..256", c.SegmentCount)
	case c.Width <= 0 || c.Height <= 0 || c.Width > 16384 || c.Height > 16384:
		return fmt.Errorf("dimensions %dx%d outside 1..16384", c.Width, c.Height)
	case c.KeyframeEvery < 1:
		return fmt.Errorf("keyframe interval must be positive")
	case c.MaxRecords < 1 || c.MaxRecords > 1_000_000:
		return fmt.Errorf("record cap %d outside 1..1000000", c.MaxRecords)
	case c.MaxFrameBytes < 1 || c.MaxFrameBytes > frames.MaxPayload:
		return fmt.Errorf("frame byte cap %d outside 1..%d", c.MaxFrameBytes, frames.MaxPayload)
	case c.FrameRateNum <= 0 || c.FrameRateDen <= 0:
		return fmt.Errorf("frame rate %d/%d must be positive", c.FrameRateNum, c.FrameRateDen)
	}
	return nil
}

// Hash returns the stable digest used to invalidate dependent stages.
func (c Config) Hash() string {
	data, err := provenance.CanonicalJSON(c)
	if err != nil {
		return ""
	}
	return provenance.HashBytesHex(data)
}

// FrameRate returns the configured rational rate.
func (c Config) FrameRate() (clock.Rational, error) {
	return clock.NewRational(c.FrameRateNum, c.FrameRateDen)
}

// FrameDurationTicks returns the nominal tick span of one frame, rounded down.
func (c Config) FrameDurationTicks() int64 {
	return c.FrameRateDen * int64(c.Timescale) / c.FrameRateNum
}

// RecoveryPolicy converts the gap settings for the recovery stage.
func (c Config) RecoveryPolicy() recovery.Policy {
	return recovery.Policy{
		MaxGapPackets:   c.MaxGapPackets,
		MaxGapEvents:    64,
		NominalTickSpan: int64(c.AudioClockRate) / 50,
	}
}

// configErr wraps a validation failure as a configuration fault.
func configErr(err error) error {
	return fault.New(fault.StagePipeline, fault.KindConfig, "%v", err)
}
