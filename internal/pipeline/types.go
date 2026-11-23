package pipeline

import (
	"mediaroute/internal/export"
	"mediaroute/internal/fixture"
	"mediaroute/internal/journal"
	"mediaroute/internal/timeline"
)

// Exit codes surfaced to the terminal adapter.
const (
	ExitOK              = 0
	ExitCorruptInput    = 2 // input or index damage, non-recoverable
	ExitRecoveryFailure = 3 // recovery or consistency failure
	ExitBadArguments    = 4 // invalid parameters
)

// Options controls one pipeline run.
type Options struct {
	Seed          int64
	LossBurst     int
	ReorderWindow int
	CrashAt       string // journal crash point, "" disables simulation
	JournalDir    string
	// CorruptIndex rewrites one index entry's checksum before validation.
	CorruptIndex bool
	// CorruptStream truncates an OBU/NAL in a committed frame to exercise
	// codec-level corruption handling.
	CorruptStream bool
}

// Metrics are the evidence counters printed by the CLI.
type Metrics struct {
	TruthPackets    int
	Accepted        int
	Duplicates      int
	Reordered       int
	Lost            int
	CongestionPeak  int // deepest bandwidth congestion wave observed
	RoundtripRate   float64
	KeyframeTotal   int // complete keyframe samples the source produced
	KeyframeHits    int // index entries that validated against them
	KeyframeHitRate float64
	Segments        int
	SegmentMatches  int
	Recovered       bool
	RepeatHashMatch bool
	Gaps            int
	TruncatedFrames int
}

// Runner executes one configured pipeline end to end.
type Runner struct {
	opts     Options
	fx       *fixture.Fixture
	coord    *journal.Coordinator
	tl       *timeline.Timeline
	manifest *export.Manifest
	metrics  Metrics
	// committedFrames maps a frame's identity to the journal stage and
	// artifact key that durably holds its bytes.
	committedFrames map[frameKey]committedArtifact
}

// frameKey identifies one reassembled frame: a track emits at most one frame
// per RTP timestamp.
type frameKey struct {
	track uint32
	rtpTS uint32
}

// committedArtifact is one published journal artifact for a frame.
type committedArtifact struct {
	stage string
	key   string
	sha   string // SHA-256 of the committed bytes
}
