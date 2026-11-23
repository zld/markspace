// Package engine wires adapters, timeline merge, streaming pipeline, offline
// rendering and manifest verification into one reusable export operation.
package engine

import (
	"fmt"
	"time"

	"mediad/manifest"
	"mediad/render"
	"mediad/timeline"
)

// Config is the validated demo/export configuration.
type Config struct {
	Duration      time.Duration
	FPS           int
	TargetBitrate int64
	QueueCapacity int
	ConsumerDelay time.Duration
	OutputDir     string
	Fixture       string // "", "truncated", "corrupt"
	Recover       bool
	BitrateTolPct float64
}

// Report is the terminal-friendly run summary.
type Report struct {
	Frames          int
	TargetBPS       int64
	ActualBPS       int64
	BitrateDeltaPct float64
	QualityMean     float64
	QualityVariance float64
	ManifestMaxErr  int64
	ManifestSHA     string
	Errors          []manifest.ErrorRecord
	RecoveredFrames int64
	FirstBadTimeNS  int64
	ManifestPath    string
	State           manifest.State
	QueueCapacity   int
	MemoryCeiling   int // capacity*maxSample + fixed buffer
}

// FixedWorkingBuffer is the constant per-pipeline working memory added on top
// of capacity*maxSampleSize, making the memory ceiling fully explicit.
const FixedWorkingBuffer = 1 << 20 // 1 MiB

func validate(cfg Config) error {
	if cfg.FPS <= 0 || cfg.FPS > render.MaxFPS {
		return fmt.Errorf("fps must be in 1..%d, got %d", render.MaxFPS, cfg.FPS)
	}
	if cfg.Duration <= 0 {
		return fmt.Errorf("duration must be positive")
	}
	if cfg.TargetBitrate <= 0 {
		return fmt.Errorf("target bitrate must be positive")
	}
	if cfg.QueueCapacity <= 0 {
		return fmt.Errorf("queue capacity must be positive")
	}
	return nil
}

// scanBad indexes corrupt events by time and returns the first bad time. A
// corrupt keyframe stops its track: callers render no further healthy claims.
func scanBad(tl *timeline.Timeline) (map[int64]bool, int64) {
	bad := map[int64]bool{}
	var first int64 = -1
	for _, ev := range tl.Events {
		if ev.Corrupt {
			bad[ev.TimeNS] = true
			if first < 0 || ev.TimeNS < first {
				first = ev.TimeNS
			}
		}
	}
	if first < 0 {
		first = 0
	}
	return bad, first
}
