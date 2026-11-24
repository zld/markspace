package pipeline

import (
	"errors"

	"mediascan/internal/clock"
	"mediascan/internal/model"
	mp4inspect "mediascan/internal/mp4"
	"mediascan/internal/stream"
)

// ErrInterrupted is returned by test hooks after a checkpoint commit.
var ErrInterrupted = errors.New("pipeline: interrupted")

// Config configures Run.
type Config struct {
	Input                  string
	CheckpointPath         string
	Resume                 bool
	MemoryMiB              int
	MediaDir               string
	InterruptAfterSegments int
}

// Result is the run outcome.
type Result struct {
	Report           *model.Report
	ExitCode         int
	ResumeFromSample int64
	ElapsedMS        int64
	MemoryPeakBytes  int64
	RollbackReason   string
	CheckpointSource string
	PrimaryOffset    int64
	PrimaryTime      int64
	DriftPPM         int64
	WrapEvents       int
}

type workItem struct {
	URI              string
	InitURI          string
	Period           int
	GlobalStart      int64
	ManifestDuration int64
}

type runner struct {
	cfg     Config
	root    string
	budget  *stream.Budget
	chunk   int
	initRes map[string]*mp4inspect.Result

	unwrap          *clock.Unwrapper
	drift           *clock.DriftEstimator
	haveOrigin      bool
	originUnwrapped int64
	originRef       int64
	driftPPM        int64
}
