package bitstream

import "mediacore/internal/fault"

// AV1 OBU types used for frame boundary detection.
const (
	obuSequenceHeader = 1
	obuTemporalDelim  = 2
	obuFrameHeader    = 3
	obuTileGroup      = 4
	obuFrame          = 6
	obuPadding        = 15
)

type obu struct {
	offset   int64
	typ      int
	payload  []byte
	temporal int
}

// av1Picture is one coded AV1 picture: a frame OBU with the preamble that
// immediately precedes it.
type av1Picture struct {
	start    int64
	key      bool
	temporal int
}

// splitOBU parses OBU headers and sizes. It rejects forbidden or reserved
// header bits and any payload that runs past the end of the stream.
func splitOBU(data []byte) ([]obu, error) {
	var out []obu
	i := 0
	for i < len(data) {
		start := i
		hdr := data[i]
		i++
		if hdr&0x80 != 0 {
			return nil, badBoundary(CodecAV1, int64(start), "OBU forbidden bit set")
		}
		typ := int(hdr>>3) & 0x0F
		ext := hdr&0x04 != 0
		hasSize := hdr&0x02 != 0
		if hdr&0x01 != 0 {
			return nil, badBoundary(CodecAV1, int64(start), "OBU reserved bit set")
		}
		temporal := -1
		if ext {
			if i >= len(data) {
				return nil, truncated(CodecAV1, int64(start), "OBU extension header")
			}
			temporal = int(data[i]>>5) & 0x07
			i++
		}
		var size int64
		if hasSize {
			v, n, err := readLEB128(data[i:])
			if err != nil {
				return nil, truncated(CodecAV1, int64(start), "OBU size field")
			}
			i += n
			size = int64(v)
			if size < 0 || size > int64(len(data)-i) {
				return nil, truncated(CodecAV1, int64(start), "OBU payload")
			}
		} else {
			size = int64(len(data) - i)
		}
		out = append(out, obu{offset: int64(start), typ: typ, payload: data[i : i+int(size)], temporal: temporal})
		i += int(size)
	}
	if len(out) == 0 {
		return nil, fault.New(fault.CodeScanNoFrames, 0, string(CodecAV1), "no OBUs found")
	}
	return out, nil
}

func readLEB128(b []byte) (uint64, int, error) {
	var v uint64
	var shift uint
	for i := 0; i < len(b) && i < 8; i++ {
		c := b[i]
		v |= uint64(c&0x7F) << shift
		if c&0x80 == 0 {
			return v, i + 1, nil
		}
		shift += 7
	}
	return 0, 0, errShort
}

// av1FrameInfo reads just enough of the uncompressed frame header to report
// show_existing_frame and whether the frame is a KEY_FRAME. A shown key frame
// is the AV1 random access point; show_existing_frame is never one.
func av1FrameInfo(payload []byte, reducedStill bool, offset int64) (showExisting, key bool, err error) {
	if len(payload) == 0 {
		return false, false, truncated(CodecAV1, offset, "frame header")
	}
	r := bitReader{data: payload}
	if !reducedStill {
		v, e := r.bits(1)
		if e != nil {
			return false, false, truncated(CodecAV1, offset, "frame header")
		}
		if v == 1 {
			return true, false, nil
		}
	}
	frameType, e := r.bits(2)
	if e != nil {
		return false, false, truncated(CodecAV1, offset, "frame header")
	}
	return false, frameType == 0, nil
}

type bitReader struct {
	data []byte
	pos  int
}

func (r *bitReader) bits(n int) (uint64, error) {
	var v uint64
	for i := 0; i < n; i++ {
		if r.pos/8 >= len(r.data) {
			return 0, errShort
		}
		bit := (r.data[r.pos/8] >> (7 - uint(r.pos%8))) & 1
		v = v<<1 | uint64(bit)
		r.pos++
	}
	return v, nil
}
