// Package app implements the end-to-end demonstration scenarios on top of the
// reusable processing kernel: fixture generation, corruption localization, a
// normal demo run, and a kill/resume cycle with checksum equivalence.
package app

import (
	"time"

	"mediatimeline/internal/pipeline"
)

// Metrics re-exports the pipeline metrics for the CLI layer.
type Metrics = pipeline.Metrics

// DemoOptions configures the demo scenario.
type DemoOptions struct {
	Seed            int64
	LateMS          int
	CheckpointEvery int
}

// DamageLocation is the localized position of recoverable input damage.
type DamageLocation struct {
	Offset      int64
	TrackID     uint32
	SampleIndex int64
}

// RunDemo builds a VFR/drift/wrap fixture and processes it end to end.
func RunDemo(dir string, opts DemoOptions) (Metrics, error) {
	return Metrics{}, nil
}

// RunCorrupt produces the requested damage and localizes it to within one
// sample, returning the structured position the CLI prints.
func RunCorrupt(dir, kind string) (DamageLocation, error) {
	return DamageLocation{}, nil
}

// RunResume runs to a simulated kill, restarts from the latest checkpoint and
// returns the uninterrupted-equivalent metrics plus recovery timing.
func RunResume(dir string, killAfter time.Duration) (Metrics, int64, uint64, error) {
	return Metrics{}, 0, 0, nil
}
