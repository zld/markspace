// Package ebml implements a bounded, streaming reader for the subset of EBML
// used by Matroska and WebM. This file holds the demuxer's public types.
package ebml

// Matroska element defaults.
const (
	// DefaultTimecodeScaleNs is 1 ms in nanoseconds, the container default.
	DefaultTimecodeScaleNs int64 = 1_000_000
)

// FileInfo holds the document-level metadata needed by the repair stage.
type FileInfo struct {
	DocType         string
	TimecodeScaleNs int64
	DurationNs      int64
	Tracks          []TrackInfo
	ClusterEvents   int
}

// TrackInfo is one TrackEntry. Only the fields the repair stage consumes are
// kept: identity, type and nominal block duration.
type TrackInfo struct {
	Number       uint64
	Type         uint64
	CodecID      string
	DefaultDurNs int64
}

// Block is one SimpleBlock.
type Block struct {
	TrackNumber uint64
	// TimecodeMs is the raw container timestamp: cluster timecode plus the
	// signed block offset, still in 32-bit millisecond space.
	TimecodeMs uint32
	Keyframe   bool
	Payload    []byte
	// BlockOffset is the absolute offset of the block element header.
	BlockOffset int64
	// ClusterOffset is the absolute offset of the enclosing cluster header.
	ClusterOffset int64
	// EndOffset is the absolute offset just past the block element, i.e. a
	// confirmed parse boundary.
	EndOffset int64
}
