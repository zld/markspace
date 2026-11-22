// Package bitstream locates H.264 Annex B NAL unit boundaries, identifies
// keyframes, and verifies keyframe indexes against rescans.
package bitstream

// NAL unit types (H.264, ISO/IEC 14496-10 Table 7-1).
const (
	NALTypeSlice = 1
	NALTypeIDR   = 5
	NALTypeSEI   = 6
	NALTypeSPS   = 7
	NALTypePPS   = 8
)

// NAL describes one Annex B NAL unit located in a buffer.
type NAL struct {
	Offset    int64 // absolute byte offset of the start code
	HeaderAt  int64 // absolute byte offset of the NAL header byte
	Type      uint8
	RefIDC    uint8
	Length    int64 // start code through end of payload
	Truncated bool  // ran to end of buffer with no following start code
}

// FindStartCode locates the next Annex B start code (0x000001 or
// 0x00000001) at or after from. It returns the index and the code size
// (3 or 4), or -1, 0 when none is found.
func FindStartCode(b []byte, from int) (idx, size int) {
	if from < 0 {
		from = 0
	}
	for i := from; i+2 < len(b); i++ {
		if b[i] == 0 && b[i+1] == 0 && b[i+2] == 1 {
			if i > from && b[i-1] == 0 {
				return i - 1, 4
			}
			return i, 3
		}
	}
	return -1, 0
}

// ParseHeader decodes a NAL header byte into its type, nal_ref_idc and
// forbidden_zero_bit.
func ParseHeader(b byte) (nalType, refIDC, forbidden uint8) {
	return b & 0x1f, (b >> 5) & 0x03, b >> 7
}
