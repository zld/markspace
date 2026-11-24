package demux

import (
	"encoding/binary"
	"sort"

	"mediapipeline/internal/fault"
)

// trunTemplate records the byte width layout of one trun so a resumed scan can
// distinguish a plausible fragment from random media bytes.
type trunLayout struct {
	trackID     uint32
	dataOffset  int64
	sampleCount uint32
	firstDTS    uint64
	timescale   uint32
}

// ScanFragments sequentially recovers moof/mdat fragments without trusting
// any index.  It returns the fragments that were structurally complete, and
// the byte range of the first incomplete fragment as the incomplete range.
// The scan starts at start and stops at the first byte where a fragment cannot
// be completed; everything before that point is verified.
func ScanFragments(data []byte, start int64, opts Options) (*FragmentScan, error) {
	if start < 0 || start > int64(len(data)) {
		return nil, fault.New(fault.StageDemux, "mp4", fault.OpDemuxInvalid, start, "resume offset outside input")
	}
	scan := &FragmentScan{Format: "mp4"}
	pos := start
	for pos+8 <= int64(len(data)) {
		size32 := int64(binary.BigEndian.Uint32(data[pos:]))
		typ := string(data[pos+4 : pos+8])
		if typ != "moof" {
			// Skip unknown bytes one box at a time when they are well formed;
			// otherwise report the exact byte where the structure broke.
			if size32 < 8 || pos+size32 > int64(len(data)) {
				scan.Incomplete = []Range{{Start: pos, End: int64(len(data))}}
				return scan, nil
			}
			pos += size32
			continue
		}
		if size32 < 8 {
			scan.Incomplete = []Range{{Start: pos, End: pos + 8}}
			return scan, nil
		}
		moofEnd := pos + size32
		if moofEnd > int64(len(data)) {
			scan.Incomplete = []Range{{Start: int64(len(data)), End: moofEnd}}
			return scan, nil
		}
		boxes, cut, err := parseBoxes(data, pos, moofEnd, "mp4", false)
		if err != nil || len(cut) > 0 || len(boxes) != 1 {
			return scan, err
		}
		moof := boxes[0]
		next := moofEnd
		if next+8 > int64(len(data)) {
			scan.Incomplete = []Range{{Start: int64(len(data)), End: next + 8}}
			return scan, nil
		}
		mdatSize := int64(binary.BigEndian.Uint32(data[next:]))
		mdatType := string(data[next+4 : next+8])
		if mdatType != "mdat" || mdatSize < 8 {
			scan.Incomplete = []Range{{Start: next, End: next + 8}}
			return scan, nil
		}
		mdatEnd := next + mdatSize
		if mdatEnd > int64(len(data)) {
			scan.Incomplete = []Range{{Start: int64(len(data)), End: mdatEnd}}
			return scan, nil
		}
		layout, err := parseFragment(moof, next+8, mdatEnd, nil)
		if err != nil {
			if fe, ok := fault.As(err); ok && fe.Operation == fault.OpTruncated {
				scan.Incomplete = []Range{{Start: int64(len(data)), End: mdatEnd}}
				return scan, nil
			}
			return scan, err
		}
		seg := Segment{
			ID:         len(scan.Segments),
			MoofOffset: pos,
			DataOffset: next + 8,
			Size:       mdatEnd - pos,
			Tracks:     append([]uint32(nil), layout.trackIDs...),
			Payload:    data[next+8 : mdatEnd],
		}
		sort.Slice(seg.Tracks, func(i, j int) bool { return seg.Tracks[i] < seg.Tracks[j] })
		scan.Segments = append(scan.Segments, seg)
		for _, id := range seg.Tracks {
			scan.TrackIDs = appendUnique(scan.TrackIDs, id)
		}
		pos = mdatEnd
	}
	if pos < int64(len(data)) {
		scan.Incomplete = []Range{{Start: pos, End: int64(len(data))}}
	}
	return scan, nil
}

// SegmentsPayloadHashInput returns the ordered payload hashes so a resumed run
// can detect duplicated or reordered commits.
func (s *FragmentScan) SegmentIDs() []int {
	ids := make([]int, len(s.Segments))
	for i := range s.Segments {
		ids[i] = s.Segments[i].ID
	}
	return ids
}
