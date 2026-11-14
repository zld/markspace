// Package config defines and validates the transcode pipeline configuration.
package config

import (
	"errors"
	"fmt"
	"time"
)

// Safety bounds for operator-supplied parameters.
const (
	MaxConcurrency = 16
	MaxRetries     = 5
	MaxSegment     = time.Hour
	MinTimeout     = time.Second
)

var allowedPresets = map[string]bool{
	"ultrafast": true, "superfast": true, "veryfast": true, "faster": true,
	"fast": true, "medium": true, "slow": true, "slower": true, "veryslow": true,
}

// Config controls one pipeline run. The pixel format and frame-rate policy are
// fixed (yuv420p, constant frame rate) so quality numbers stay comparable
// across runs; they are recorded in the report.
type Config struct {
	OutDir      string
	Segment     time.Duration
	TargetKbps  int
	MaxrateKbps int // peak constraint (-maxrate/-bufsize); defaults to 2x target
	Concurrency int
	Retries     int
	Timeout     time.Duration // per external-process timeout
	FPS         int
	PixFmt      string
	Preset      string
}

// Default returns a Config with sane defaults; CLI flags override fields.
func Default() Config {
	return Config{
		OutDir:      "./artifacts",
		Segment:     2 * time.Second,
		TargetKbps:  900,
		Concurrency: 2,
		Retries:     2,
		Timeout:     2 * time.Minute,
		FPS:         30,
		PixFmt:      "yuv420p",
		Preset:      "veryfast",
	}
}

// Validate rejects non-positive bitrates, invalid segment lengths, empty
// output dirs and out-of-range concurrency before any work starts.
func (c *Config) Validate() error {
	if c.OutDir == "" {
		return errors.New("config: output directory must not be empty")
	}
	if c.TargetKbps <= 0 {
		return fmt.Errorf("config: target bitrate must be positive, got %d kbps", c.TargetKbps)
	}
	if c.MaxrateKbps == 0 {
		c.MaxrateKbps = 2 * c.TargetKbps
	}
	if c.MaxrateKbps < c.TargetKbps {
		return fmt.Errorf("config: maxrate %d kbps is below target %d kbps", c.MaxrateKbps, c.TargetKbps)
	}
	if c.Segment <= 0 || c.Segment > MaxSegment {
		return fmt.Errorf("config: segment length %s outside (0, %s]", c.Segment, MaxSegment)
	}
	if c.Concurrency < 1 || c.Concurrency > MaxConcurrency {
		return fmt.Errorf("config: concurrency %d outside [1, %d]", c.Concurrency, MaxConcurrency)
	}
	if c.Retries < 0 || c.Retries > MaxRetries {
		return fmt.Errorf("config: retries %d outside [0, %d]", c.Retries, MaxRetries)
	}
	if c.Timeout < MinTimeout {
		return fmt.Errorf("config: process timeout %s below minimum %s", c.Timeout, MinTimeout)
	}
	if c.FPS < 1 || c.FPS > 120 {
		return fmt.Errorf("config: fps %d outside [1, 120]", c.FPS)
	}
	if c.PixFmt != "yuv420p" {
		return fmt.Errorf("config: only yuv420p pixel format is supported, got %q", c.PixFmt)
	}
	if !allowedPresets[c.Preset] {
		return fmt.Errorf("config: unknown x264 preset %q", c.Preset)
	}
	return nil
}
