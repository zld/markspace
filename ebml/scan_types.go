package ebml

// MaxTracks bounds the number of track entries accepted in one stream.
const MaxTracks = 32

// ColorMeta is a colour record plus the element ID that carried it, so the
// resolver can attribute a source and retain unknown enumerations verbatim.
type ColorMeta struct {
	Matrix        uint32
	Transfer      uint32
	Primaries     uint32
	RangeFlag     uint32
	RawEnum       uint32 // 0 when no unknown enumeration was present
	SourceElement ElementID
	SourceOffset  int64
}

// ChannelMapEntry is one ordered channel position in a fixture layout table.
type ChannelMapEntry struct {
	Position uint32
	Order    uint32
}

// TrackSpec is the immutable track description parsed from the Tracks list.
type TrackSpec struct {
	Number       uint32
	UID          uint32
	Type         uint8
	CodecID      string
	DefaultDurNs int64
	Width        uint32
	Height       uint32
	SampleRate   uint32
	Channels     uint32
	BitDepth     uint32
	LayoutName   string
	ChannelMap   []ChannelMapEntry
	Color        *ColorMeta
}

// IsVideo reports whether the track carries uncompressed test frames.
func (t *TrackSpec) IsVideo() bool { return t.Type == TrackTypeVideo }

// IsAudio reports whether the track carries interleaved PCM samples.
func (t *TrackSpec) IsAudio() bool { return t.Type == TrackTypeAudio }

// BlockInfo records one SimpleBlock with timeline and integrity context.
type BlockInfo struct {
	TrackNumber uint32
	TimeNs      int64
	Offset      int64
	Size        int64
	Payload     []byte
	ClusterOff  int64
	ClusterTime int64
	CheckOK     bool // cluster CRC covering this block verified
}

// ScanResult is the full scan output. On truncation the returned error is
// non-nil while every element parsed before the fault remains populated.
type ScanResult struct {
	TimeScaleNs    int64
	ContainerColor *ColorMeta
	Tracks         []*TrackSpec
	Blocks         []*BlockInfo
	SegmentOff     int64
	SegmentLive    bool
	TruncatedAt    int64 // -1 when the stream was fully parseable
	CheckFailures  []int64
	BlockCount     int
}
