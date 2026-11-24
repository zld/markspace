package pipeline

import "mediarun/internal/verify"

// Options configures one pipeline run.
type Options struct {
	InputDir      string
	OutDir        string
	TargetBitrate int64
	Resume        bool
}

// Result is what a successful run reports to the terminal.
type Result struct {
	Metrics      verify.Metrics
	Report       *Report
	OutputDigest string
	ReusedStages int
}
