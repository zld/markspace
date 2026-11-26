// Package replay is the reusable local processing core: it parses the two
// container formats, repairs the timeline, maps RTP events to windows,
// re-packages compressed samples, publishes an index and digests the result.
// The command line is a thin wrapper over this package.
package replay

import (
	"fmt"

	"replayproof/internal/media"
)

// Selectors maps metadata field names (rotation, crop, color) to the input
// label that wins a conflict.
type Selectors map[string]string

// Inputs are the generated or supplied containers for one run. The primary
// pair is processed; the corrupt and conflicting sets are rejected and
// recorded, which is how the program proves its failure paths.
type Inputs struct {
	PrimaryMP4 []byte
	PrimaryMXF []byte
	// CorruptMP4 is truncated inside its first box.
	CorruptMP4 []byte
	// CorruptMXF has a KLV at offset zero claiming bytes beyond the file.
	CorruptMXF []byte
	// ConflictMXF describes metadata that disagrees with PrimaryMP4.
	ConflictMXF []byte
	// RTP packets in arrival order; marker packets trigger events.
	RTP [][]byte
}

// Config is the full parameter set for one run.
type Config struct {
	Seed      int64
	Workers   int
	Queue     int
	DelayNS   int64
	PreRoll   media.Rational
	PostRoll  media.Rational
	MaxDev    media.Rational
	Selectors Selectors
	ClockRate uint32
	// OutDir receives input copies, clips, the index and the summary.
	OutDir string
	// CheckpointPath enables resume when non-empty.
	CheckpointPath string
	// CancelAfter, when > 0, cancels the pipeline after that many released
	// clips, exercising consumer-to-producer cancellation.
	CancelAfter int
}

// DefaultConfig returns the parameters used by `replaycheck verify`.
func DefaultConfig() Config {
	return Config{
		Seed:    7,
		Workers: 3,
		Queue:   2,
		// Pre-roll reaches back at most one keyframe interval so each clip is
		// independently decodable; post-roll extends past the event. Together
		// the two default windows cover the whole 4 s fixture program.
		PreRoll:   media.FromMillis(1000),
		PostRoll:  media.FromMillis(1200),
		MaxDev:    media.FromMicros(11_112),
		ClockRate: 90000,
		Selectors: Selectors{"rotation": "mp4"},
	}
}

// Validate checks the parameter set before any file is touched.
func (c Config) Validate() error {
	if c.Workers < 1 {
		return media.New(media.CodeConfig, "config.workers", -1, "workers must be >= 1")
	}
	if c.Queue < 1 {
		return media.New(media.CodeConfig, "config.queue", -1, "queue must be >= 1")
	}
	if c.OutDir == "" {
		return media.New(media.CodeConfig, "config.out", -1, "output directory is required")
	}
	if c.PreRoll.Sign() < 0 || c.PostRoll.Sign() < 0 {
		return media.New(media.CodeConfig, "config.roll", -1, "pre/post roll must be non-negative")
	}
	if c.ClockRate == 0 {
		return media.New(media.CodeConfig, "config.clock", -1, "clock rate is required")
	}
	for field := range c.Selectors {
		switch field {
		case "rotation", "crop", "color":
		default:
			return media.New(media.CodeConfig, "config.selector", -1, "unknown metadata field "+field)
		}
	}
	if c.CancelAfter < 0 {
		return media.New(media.CodeConfig, "config.cancel", -1, "cancel-after must be >= 0")
	}
	return nil
}

// fmtDur renders a duration for log lines.
func fmtDur(ns int64) string { return fmt.Sprintf("%dns", ns) }
