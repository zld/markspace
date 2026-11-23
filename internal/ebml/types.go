// Package ebml parses the subset of Matroska/WebM EBML structures needed for
// archive recovery: unknown-length Segment/Cluster containers, interleaved
// Block/SimpleBlock elements and explicit truncation reporting. It never
// reads past a declared boundary or the configured size limits.
package ebml

import (
	"bytes"
	"fmt"
	"io"

	"mediaarchive/internal/timeline"
)

// Matroska element identifiers used by the parser.
var (
	IDEBML      = []byte{0x1A, 0x45, 0xDF, 0xA3}
	IDSegment   = []byte{0x18, 0x53, 0x80, 0x67}
	IDCluster   = []byte{0x1F, 0x43, 0xB6, 0x75}
	IDTimecode  = []byte{0xE7}
	IDSimpleBlk = []byte{0xA3}
	IDBlockGrp  = []byte{0xA0}
	IDBlock     = []byte{0xA1}
)

// DefaultLimits enforces the archive policy: single files up to 4 GiB,
// individual elements up to 2 GiB, blocks up to 64 MiB.
func DefaultLimits() Limits {
	return Limits{
		MaxFileSize:    4 << 30,
		MaxElementSize: 2 << 30,
		MaxBlockSize:   64 << 20,
	}
}

// Limits caps every size the parser is willing to trust from the input.
type Limits struct {
	MaxFileSize    int64
	MaxElementSize int64
	MaxBlockSize   int64
}

// Element describes one parsed EBML element header plus its payload span.
type Element struct {
	ID            []byte
	Offset        int64 // offset of the ID byte
	HeaderSize    int64
	Size          int64 // -1 means unknown length
	PayloadOffset int64
	End           int64 // exclusive, -1 when unknown
	Truncated     bool  // declared size ran past the real EOF
}

// BlockEvent is delivered to the timeline layer for every parsed block.
type BlockEvent struct {
	Track      uint32
	TimeNanos  int64
	Flags      byte
	Keyframe   bool
	Payload    []byte
	SourceSeq  int64 // input order, guarantees stable merging
	FileOffset int64
}

// ParseReport accumulates recoverable faults and span statistics.
type ParseReport struct {
	TruncatedClusterOffsets []int64
	OversizedElements       []int64
	Issues                  []*timeline.Error
	BlockCount              int
	FirstBlockNanos         int64
	LastBlockNanos          int64
}

// ParseResult bundles recovered events with the recovery report.
type ParseResult struct {
	Events  []BlockEvent
	Report  *ParseReport
	Segment Element
}

// Reader parses an in-memory Matroska byte stream.
type Reader struct {
	r      io.ReadSeeker
	size   int64
	limits Limits
	report *ParseReport
	seq    int64
}

// NewReader validates the global size policy and constructs a Reader.
func NewReader(data []byte, limits Limits) (*Reader, error) {
	if int64(len(data)) > limits.MaxFileSize {
		return nil, timeline.NewError("ebml", "open", -1, timeline.RecoverNone,
			fmt.Sprintf("file %d bytes exceeds limit %d", len(data), limits.MaxFileSize),
			timeline.ErrLimitExceeded)
	}
	return &Reader{
		r: bytes.NewReader(data), size: int64(len(data)),
		limits: limits, report: &ParseReport{
			FirstBlockNanos: -1, LastBlockNanos: -1,
		},
	}, nil
}

func isContainer(id []byte) bool {
	switch {
	case bytes.Equal(id, IDEBML), bytes.Equal(id, IDSegment),
		bytes.Equal(id, IDCluster), bytes.Equal(id, IDBlockGrp):
		return true
	}
	return false
}
