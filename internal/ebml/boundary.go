package ebml

import "math/bits"

// atSegmentBoundary reports whether off points at a legal Segment child that
// terminates an unknown-length Cluster. Arbitrary data never terminates it.
func atSegmentBoundary(data []byte, off, segEnd int, limits Limits) bool {
	if off >= segEnd || off >= len(data) {
		return true
	}
	first := data[off]
	if first == 0 {
		return false
	}
	width := bits.LeadingZeros8(first) + 1
	if width > limits.MaxIDWidth || off+width > len(data) {
		return false
	}
	var id uint32
	for i := 0; i < width; i++ {
		id = id<<8 | uint32(data[off+i])
	}
	switch id {
	case IDCluster, IDInfo, IDTracks, IDCues, IDTags, IDChapters, IDAttachments, IDSeekHead:
		return true
	default:
		return false
	}
}
