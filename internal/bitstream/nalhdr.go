package bitstream

import "mediacore/internal/fault"

// h264FirstSlice reads first_mb_in_slice, the first Exp-Golomb field of an
// AVC slice header. It returns true when the slice starts a picture.
func h264FirstSlice(b []byte, base int64) (bool, error) {
	v, err := readExpGolomb(b)
	if err != nil {
		return false, truncated(CodecH264, base, "slice header")
	}
	return v == 0, nil
}

// h265FirstSlice reads first_slice_segment_in_pic_flag, the leading bit of an
// HEVC slice segment header.
func h265FirstSlice(b []byte, base int64) (bool, error) {
	if len(b) == 0 {
		return false, truncated(CodecH265, base, "slice header")
	}
	return b[0]&0x80 != 0, nil
}

// readExpGolomb decodes an unsigned Exp-Golomb code.
func readExpGolomb(b []byte) (uint64, error) {
	bit := func(pos int) (uint64, bool) {
		if pos/8 >= len(b) {
			return 0, false
		}
		return uint64((b[pos/8] >> (7 - uint(pos%8))) & 1), true
	}
	pos := 0
	v, ok := bit(pos)
	if !ok {
		return 0, errShort
	}
	zeros := 0
	for v == 0 {
		zeros++
		if zeros > 32 {
			return 0, errShort
		}
		pos++
		v, ok = bit(pos)
		if !ok {
			return 0, errShort
		}
	}
	result := uint64(1)
	for i := 0; i < zeros; i++ {
		pos++
		bv, ok := bit(pos)
		if !ok {
			return 0, errShort
		}
		result = result<<1 | bv
	}
	return result - 1, nil
}

type shortError struct{}

func (shortError) Error() string { return "short bitstream" }

var errShort = shortError{}

func truncated(codec Codec, offset int64, what string) error {
	return fault.New(fault.CodeScanTruncatedHeader, offset, string(codec), "%s is truncated", what)
}

func badBoundary(codec Codec, offset int64, what string) error {
	return fault.New(fault.CodeScanIllegalBoundary, offset, string(codec), "%s", what)
}
