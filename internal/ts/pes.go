package ts

import "errors"

// ErrMalformedPES reports a PES packet whose fixed header is invalid.
var ErrMalformedPES = errors.New("malformed PES packet")

// Frame carries the decoded result of one video access unit.
type Frame struct {
	// PTS is the unwrapped 90 kHz presentation timestamp.
	PTS int64
	// DTS is the unwrapped decode timestamp; equals PTS when absent.
	DTS int64
	// Keyframe is true for an H.264 IDR access unit.
	Keyframe bool
	// StartOffset is the stream byte offset of the first TS packet of the
	// PES packet carrying this frame.
	StartOffset int64
	// EndOffset is one past the last TS packet belonging to this frame.
	EndOffset int64
}

// ParsePESHeader consumes a PUSI payload containing a PES packet header and
// returns the raw elementary stream bytes plus the embedded PTS/DTS (0 when
// a timestamp is absent). It handles the standard start code 0x000001.
func ParsePESHeader(payload []byte) (es []byte, pts, dts int64, err error) {
	if len(payload) < 9 || payload[0] != 0x00 || payload[1] != 0x00 || payload[2] != 0x01 {
		return nil, 0, 0, ErrMalformedPES
	}
	// payload[3] stream id; payload[4:6] PES packet length.
	hdrDataLen := int(payload[8])
	if 9+hdrDataLen > len(payload) {
		return nil, 0, 0, ErrMalformedPES
	}
	flags := payload[7]
	prefix := payload[9] >> 4
	pos := 9
	if flags&0x80 != 0 { // PTS_DTS_flags
		if pos+5 > 9+hdrDataLen {
			return nil, 0, 0, ErrMalformedPES
		}
		pts = decodeTimestamp(payload[pos : pos+5])
		pos += 5
		if prefix == 3 { // both PTS and DTS present
			if pos+5 > 9+hdrDataLen {
				return nil, 0, 0, ErrMalformedPES
			}
			dts = decodeTimestamp(payload[pos : pos+5])
		} else {
			dts = pts
		}
	}
	return payload[9+hdrDataLen:], pts, dts, nil
}

// decodeTimestamp reads the 40-bit '0010xxxx' / '0011xxxx' timestamp field
// defined in ISO 13818-1 and returns the 33-bit value.
func decodeTimestamp(b []byte) int64 {
	return int64(b[0]&0x0E)<<29 |
		int64(b[1])<<22 |
		int64(b[2]&0xFE)<<14 |
		int64(b[3])<<7 |
		int64(b[4]>>1)
}

// AnnexBPrefix is the 3-byte NAL unit start code.
var AnnexBPrefix = []byte{0x00, 0x00, 0x01}

// IsIDRAccessUnit inspects the beginning of an Annex-B elementary stream
// buffer and reports whether the access unit starts with an IDR slice NAL.
// SPS/PPS NALs preceding the slice (typical at each IDR) are skipped.
func IsIDRAccessUnit(es []byte) bool {
	for _, nal := range SplitAnnexB(es) {
		if len(nal) == 0 {
			continue
		}
		nalType := nal[0] & 0x1F // H.264 nal_unit_type
		switch nalType {
		case 1, 2, 5:
			// First VCL NAL decides; 5 = coded slice of an IDR picture.
			return nalType == 5
		}
	}
	return false
}

// SplitAnnexB splits an elementary stream buffer into NAL units, accepting
// both 3-byte (0x000001) and 4-byte (0x00000001) start codes.
func SplitAnnexB(es []byte) [][]byte {
	var nals [][]byte
	var starts []int
	for i := 0; i+3 <= len(es); {
		if es[i] == 0 && es[i+1] == 0 && es[i+2] == 1 {
			starts = append(starts, i+3)
			i += 3
			continue
		}
		if i+3 < len(es) && es[i] == 0 && es[i+1] == 0 &&
			es[i+2] == 0 && es[i+3] == 1 {
			starts = append(starts, i+4)
			i += 4
			continue
		}
		i++
	}
	for i, start := range starts {
		end := len(es)
		if i+1 < len(starts) {
			// The next start code's leading zeros belong to the delimiter.
			end = starts[i+1]
			for end > start && es[end-1] == 0 {
				end--
			}
		}
		nals = append(nals, es[start:end])
	}
	return nals
}
