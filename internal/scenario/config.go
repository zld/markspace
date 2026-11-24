// Package scenario wires the self-generated RTP, metadata, and MP4 fixtures
// through avpipe's recovery, parsing, alignment, DAG, manifest, and metrics
// kernels. It contains no external files or network dependencies.
package scenario

import (
	"github.com/zld/avpipe/internal/dag"
	"github.com/zld/avpipe/internal/metrics"
	"github.com/zld/avpipe/internal/mp4"
	"github.com/zld/avpipe/internal/rtp"
)

// Config are the terminal-controllable demo parameters.
type Config struct {
	Seed            int64
	Loss            float64
	Truncate        int
	MissingIndex    bool
	Segments        int
	Representations int
	Packets         int
	ReorderWindow   int
	MemoryMB        int
	MaxRetries      int
	OutDir          string
	FailNode        string
}

// DefaultConfig returns deterministic local demo settings.
func DefaultConfig() Config {
	return Config{
		Seed:            7,
		Loss:            0,
		Truncate:        0,
		MissingIndex:    false,
		Segments:        6,
		Representations: 3,
		Packets:         120,
		ReorderWindow:   32,
		MemoryMB:        64,
		MaxRetries:      2,
		OutDir:          "out",
	}
}

// Result retains auditable subsystem results and terminal metrics.
type Result struct {
	Config          Config
	Metrics         metrics.Report
	DAG             *dag.Report
	RTPStats        rtp.Stats
	MP4Reports      []*mp4.Report
	CorruptReport   *mp4.Report
	ManifestSHA256  string
	ManifestPath    string
	AlignmentErrors int
}
