// Package media holds the payload-neutral data types shared by every stage of
// the pipeline. Nothing here decodes or re-encodes frame payloads.
package media

// TrackID is an EBML TrackNumber.
type TrackID uint64

// TrackType mirrors the Matroska TrackType element.
type TrackType uint8

const (
	TrackVideo TrackType = 1
	TrackAudio TrackType = 2
)

// Track describes a demuxed track.
type Track struct {
	ID       TrackID
	Type     TrackType
	Codec    string
	Name     string
	Language string
}

// Block is one decoded block header plus its opaque payload.
//
// Seq is a monotonically increasing identifier assigned in demux order and is
// used as the RTP sequence-number source. ElementOffset and ClusterOffset are
// absolute byte offsets in the source file and are what the random-access
// index points at.
type Block struct {
	Track         TrackID
	TrackType     TrackType
	Seq           uint64
	TimeNs        int64 // repaired time on the shared clock
	RawNs         int64 // unwrapped timestamp before drift correction
	Keyframe      bool
	Data          []byte
	ElementOffset int64
	EndOffset     int64
	ClusterOffset int64
	Rep           string
}

// Size is the payload size used for fan-out buffer accounting.
func (b Block) Size() int { return len(b.Data) }

// IndexEntry is a random-access index point. Every entry points at a
// keyframe block and is sorted by track then time.
type IndexEntry struct {
	Track         TrackID
	TimeNs        int64
	ByteOffset    int64
	ClusterOffset int64
}

// Index is a deterministic random-access index.
type Index struct {
	Entries []IndexEntry
	Digest  string
}
