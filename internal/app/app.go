// Package app implements the end-to-end demonstration scenarios on top of the
// reusable processing kernel: fixture generation, corruption localization, a
// normal demo run, and a kill/resume cycle with checksum equivalence.
package app

import "mediatimeline/internal/pipeline"

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
