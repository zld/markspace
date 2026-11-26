package demo

import (
	"time"

	"assetpack/internal/clock"
	"assetpack/internal/release"
	"assetpack/internal/stream"
)

// Options configures one deterministic demo publication.
type Options struct {
	Out            string
	Sequence       int
	Range          clock.Range
	QueueBlocks    int
	SlowConsumerMS int
	Fault          string
	CancelAfter    time.Duration
}

// Result contains values used by the terminal report and verification.
type Result struct {
	Commit        *release.CommitResult
	Stats         stream.PipelineStats
	DesktopCount  int
	MobileCount   int
	AudioSamples  int
	Frame12Path   string
	Frame12Hash   string
	SegmentHashes []string
}
