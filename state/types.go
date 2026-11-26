package state

import (
	"archivecheck/metrics"
	"archivecheck/pipe"
	rtpingest "archivecheck/rtp"
)

// Config configures one local archive-processing run.
type Config struct {
	WorkDir         string
	Queue           int
	MemoryBytes     int64
	SlowMS          int
	InterruptCursor int64
}

// Result contains reportable metrics and recovery details.
type Result struct {
	Report               metrics.Report
	ArchivePath          string
	ArchiveSHA256        string
	CompleteThroughHLS   int
	DASHGroupsResolved   int
	PeakInFlightBytes    int64
	LastCheckpointCursor int64
}

type workItem struct {
	rec       pipe.Record
	dashIndex int
	hlsIndex  int
	event     *rtpingest.Event
}

// ArchiveItem is one stable, content-addressed archive ledger entry.
type ArchiveItem struct {
	Cursor int64  `json:"cursor"`
	Kind   string `json:"kind"`
	Source string `json:"source"`
	Digest string `json:"digest"`
	Status string `json:"status"`
	Detail string `json:"detail,omitempty"`
}
