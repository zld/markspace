package ebml

import (
	"sort"

	"mediad/internal/clock"
)

// IDReferenceBlock marks backward references inside a BlockGroup.
const IDReferenceBlock = 0xFB

// Event is one deinterleaved block with full source localization.
type Event struct {
	Track    uint64
	Tick     int64 // absolute tick: cluster timecode + block timecode
	Keyframe bool
	Offset   int64  // file offset of the block element
	Size     int64  // total element size including header
	Duration uint64 // in ticks; 0 when not declared
}

// SegmentInfo extracts the timecode scale and declared track numbers.
func SegmentInfo(seg *Node) (timecodeScale uint64, tracks []uint64, err error) {
	timecodeScale = 1_000_000 // Matroska default
	if info := seg.Find(IDInfo); info != nil {
		if ts := info.Find(IDTimecodeScale); ts != nil {
			v, err := ts.Uint()
			if err != nil {
				return 0, nil, err
			}
			if v == 0 {
				return 0, nil, clock.At(clock.ErrZeroScale, ts.Offset, ts.Path, "timecode scale is zero")
			}
			timecodeScale = v
		}
	}
	if trks := seg.Find(IDTracks); trks != nil {
		for _, te := range trks.FindAll(IDTrackEntry) {
			tn := te.Find(IDTrackNumber)
			if tn == nil {
				return 0, nil, clock.At(clock.ErrCorruptBlock, te.Offset, te.Path, "track entry without number")
			}
			v, err := tn.Uint()
			if err != nil {
				return 0, nil, err
			}
			tracks = append(tracks, v)
		}
	}
	if len(tracks) == 0 {
		return 0, nil, clock.At(clock.ErrUnmappableTrack, seg.Offset, seg.Path, "no tracks declared")
	}
	return timecodeScale, tracks, nil
}

// Events walks all clusters of a parsed Segment and returns deinterleaved
// block events sorted by (Tick, Offset): the unified tick orders events
// across tracks, and the source offset breaks ties deterministically.
func Events(seg *Node) ([]Event, error) {
	var out []Event
	for _, clu := range seg.FindAll(IDCluster) {
		var base int64
		if tc := clu.Find(IDTimecode); tc != nil {
			v, err := tc.Uint()
			if err != nil {
				return nil, err
			}
			base = int64(v)
		}
		for _, sb := range clu.FindAll(IDSimpleBlock) {
			ev, err := blockEvent(sb, base, true)
			if err != nil {
				return nil, err
			}
			out = append(out, ev)
		}
		for _, bg := range clu.FindAll(IDBlockGroup) {
			blk := bg.Find(IDBlock)
			if blk == nil {
				return nil, clock.At(clock.ErrCorruptBlock, bg.Offset, bg.Path, "block group without block")
			}
			ev, err := blockEvent(blk, base, false)
			if err != nil {
				return nil, err
			}
			ev.Keyframe = bg.Find(IDReferenceBlock) == nil
			if bd := bg.Find(IDBlockDuration); bd != nil {
				d, err := bd.Uint()
				if err != nil {
					return nil, err
				}
				ev.Duration = d
			}
			out = append(out, ev)
		}
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].Tick != out[j].Tick {
			return out[i].Tick < out[j].Tick
		}
		return out[i].Offset < out[j].Offset
	})
	return out, nil
}

// blockEvent parses one Block/SimpleBlock element into an Event.
func blockEvent(n *Node, clusterBase int64, simple bool) (Event, error) {
	data := n.Data
	if len(data) < 4 {
		return Event{}, clock.At(clock.ErrCorruptBlock, n.Offset, n.Path,
			"block payload %d bytes, need at least 4", len(data))
	}
	track, nBytes, err := readTrackNumber(data, n.Offset, n.Path)
	if err != nil {
		return Event{}, err
	}
	rest := data[nBytes:]
	if len(rest) < 3 {
		return Event{}, clock.At(clock.ErrCorruptBlock, n.Offset, n.Path,
			"block header truncated after track number")
	}
	tc := int16(uint16(rest[0])<<8 | uint16(rest[1]))
	flags := rest[2]
	if flags&0x06 != 0 {
		return Event{}, clock.At(clock.ErrUnsupported, n.Offset, n.Path,
			"laced blocks not supported (flags 0x%02x)", flags)
	}
	keyframe := !simple || flags&0x80 != 0
	return Event{
		Track:    track,
		Tick:     clusterBase + int64(tc),
		Keyframe: keyframe,
		Offset:   n.Offset,
		Size:     n.HeaderSize + n.Size,
	}, nil
}

// readTrackNumber decodes the block's track-number VINT (marker stripped).
func readTrackNumber(data []byte, offset int64, path string) (uint64, int, error) {
	b := data[0]
	if b == 0 {
		return 0, 0, clock.At(clock.ErrInvalidVINT, offset, path, "track number starts with 0x00")
	}
	length := 1
	for mask := byte(0x80); b&mask == 0; mask >>= 1 {
		length++
	}
	if length > len(data) {
		return 0, 0, clock.At(clock.ErrCorruptBlock, offset, path, "track number VINT truncated")
	}
	val := uint64(b & (0xFF >> length))
	for i := 1; i < length; i++ {
		val = val<<8 | uint64(data[i])
	}
	return val, length, nil
}
