// Package matroska decodes interleaved blocks from a Matroska/WebM
// segment into integer-nanosecond timestamps on the shared clock.
// Blocks at the same timestamp are ordered stably by track number and
// block sequence; timestamp overflow is an error, never a guess.
package matroska

import (
	"encoding/binary"
	"sort"

	"continuity/internal/cerr"
	"continuity/internal/clock"
	"continuity/internal/ebml"
)

// ColourInfo carries the video colour metadata of a track.
type ColourInfo struct {
	Matrix    uint64
	Transfer  uint64
	Primaries uint64
	Range     uint64
	Present   bool
}

// TrackInfo describes one track of the segment.
type TrackInfo struct {
	Number     uint64
	Type       uint64 // 1 = video, 2 = audio
	Channels   uint64
	SampleRate float64
	Colour     ColourInfo
}

// Block is one de-interleaved media block on the nanosecond timeline.
type Block struct {
	TrackNumber uint64
	Index       uint64 // sequence of the block within the file
	TimestampNs int64
	DurationNs  int64
	Keyframe    bool
	Payload     []byte
}

// Segment is the parsed result: timecode scale, tracks and blocks.
type Segment struct {
	TimecodeScale uint64
	Tracks        []TrackInfo
	Blocks        []Block
}

// Parse decodes a Matroska/WebM document. On error the partially parsed
// segment is returned so callers can keep the recoverable prefix.
func Parse(data []byte) (*Segment, error) {
	seg := &Segment{TimecodeScale: 1000000}
	r := ebml.New(data)
	var clusterTime int64
	var blockIndex uint64
	var cur *TrackInfo
	lastBlock := -1
	err := r.Walk(func(el ebml.Element, data []byte) (bool, error) {
		switch el.Path {
		case "/EBML", "/Segment", "/Segment/Info", "/Segment/Tracks",
			"/Segment/Tracks/TrackEntry/Audio",
			"/Segment/Tracks/TrackEntry/Video",
			"/Segment/Tracks/TrackEntry/Video/Colour",
			"/Segment/Cluster", "/Segment/Cluster/BlockGroup":
			return true, nil
		case "/Segment/Tracks/TrackEntry":
			seg.Tracks = append(seg.Tracks, TrackInfo{})
			cur = &seg.Tracks[len(seg.Tracks)-1]
			return true, nil
		case "/Segment/Info/TimecodeScale":
			seg.TimecodeScale = ebml.Uint(data)
		case "/Segment/Tracks/TrackEntry/TrackNumber":
			cur.Number = ebml.Uint(data)
		case "/Segment/Tracks/TrackEntry/TrackType":
			cur.Type = ebml.Uint(data)
		case "/Segment/Tracks/TrackEntry/Audio/Channels":
			cur.Channels = ebml.Uint(data)
		case "/Segment/Tracks/TrackEntry/Audio/SamplingFrequency":
			cur.SampleRate = ebml.Float(data)
		case "/Segment/Tracks/TrackEntry/Video/Colour/MatrixCoefficients":
			cur.Colour.Matrix = ebml.Uint(data)
			cur.Colour.Present = true
		case "/Segment/Tracks/TrackEntry/Video/Colour/TransferCharacteristics":
			cur.Colour.Transfer = ebml.Uint(data)
		case "/Segment/Tracks/TrackEntry/Video/Colour/Primaries":
			cur.Colour.Primaries = ebml.Uint(data)
		case "/Segment/Tracks/TrackEntry/Video/Colour/Range":
			cur.Colour.Range = ebml.Uint(data)
		case "/Segment/Cluster/Timecode":
			clusterTime = int64(ebml.Uint(data))
		case "/Segment/Cluster/SimpleBlock", "/Segment/Cluster/BlockGroup/Block":
			b, err := parseBlock(el, data, clusterTime, seg.TimecodeScale, blockIndex)
			if err != nil {
				return false, err
			}
			seg.Blocks = append(seg.Blocks, b)
			lastBlock = len(seg.Blocks) - 1
			blockIndex++
		case "/Segment/Cluster/BlockGroup/BlockDuration":
			if lastBlock >= 0 {
				d, err := clock.ToNanos(int64(ebml.Uint(data)), seg.TimecodeScale)
				if err != nil {
					return false, cerr.New(cerr.CodeTimestampOverflow, el.Offset, el.Path, "block duration overflows nanoseconds")
				}
				seg.Blocks[lastBlock].DurationNs = d
			}
		}
		return false, nil
	})
	if err != nil {
		return seg, err
	}
	// Deterministic order: timestamp, then track number, then block
	// sequence. SliceStable plus a total comparator keeps it stable.
	sort.SliceStable(seg.Blocks, func(i, j int) bool {
		a, b := seg.Blocks[i], seg.Blocks[j]
		if a.TimestampNs != b.TimestampNs {
			return a.TimestampNs < b.TimestampNs
		}
		if a.TrackNumber != b.TrackNumber {
			return a.TrackNumber < b.TrackNumber
		}
		return a.Index < b.Index
	})
	return seg, nil
}

func parseBlock(el ebml.Element, data []byte, clusterTime int64, scale uint64, index uint64) (Block, error) {
	var b Block
	b.Index = index
	if len(data) < 4 {
		return b, cerr.New(cerr.CodeTruncatedData, el.Offset, el.Path, "block header truncated")
	}
	track, n, err := ebml.VINTValue(data)
	if err != nil {
		return b, cerr.New(cerr.CodeVINTInvalid, el.Offset, el.Path, "invalid track number VINT")
	}
	if len(data) < n+3 {
		return b, cerr.New(cerr.CodeTruncatedData, el.Offset, el.Path, "block header truncated")
	}
	tc := int64(int16(binary.BigEndian.Uint16(data[n:])))
	flags := data[n+2]
	if flags&0x06 != 0 {
		return b, cerr.New(cerr.CodeLacingUnsupported, el.Offset, el.Path, "laced blocks are not supported")
	}
	b.TrackNumber = track
	b.Keyframe = flags&0x80 != 0
	ticks := clusterTime + tc
	if (tc > 0 && ticks < clusterTime) || (tc < 0 && ticks > clusterTime) {
		return b, cerr.New(cerr.CodeTimestampOverflow, el.Offset, el.Path, "block timecode overflows int64")
	}
	ns, err := clock.ToNanos(ticks, scale)
	if err != nil {
		return b, cerr.New(cerr.CodeTimestampOverflow, el.Offset, el.Path, "block timestamp overflows nanoseconds")
	}
	b.TimestampNs = ns
	b.Payload = data[n+3:]
	return b, nil
}
