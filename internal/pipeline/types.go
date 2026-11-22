package pipeline

import "archivist/internal/model"

// Config is the fully-resolved demo configuration.
type Config struct {
	Seed       int64
	Bitrates   []uint32
	SegmentMS  uint32
	Corruption string
	Retries    int
	OutputDir  string
}

// Metrics holds every quantitative field the acceptance demo must print.
type Metrics struct {
	RoundtripRate     float64
	KeyframeHitRate   float64
	Segments          int
	AlignedBoundaries string // "n/m"
	SegmentHashMatch  string // "n/m"
	RecoveredIndexes  int
	BlockedBy         string
	ManifestWritten   bool
	RerunDigestEqual  bool
	Decisions         []model.Decision
	Warnings          []model.Warning
	Digest            string
	ExitCode          int
}

// Runner executes the full pipeline on a self-generated fixture.
type Runner struct {
	cfg Config
}

// New constructs a pipeline runner.
func New(cfg Config) *Runner { return &Runner{cfg: cfg} }
