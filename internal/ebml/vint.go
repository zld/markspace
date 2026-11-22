package ebml

import "math"

// UnknownSize is the sentinel returned for elements encoded with the EBML
// unknown-length marker.
const UnknownSize = int64(-1)

// readFull wraps io.ReadFull with position accounting. Any EOF while bytes
// were expected is KindTruncated; the clean EOF path in Next distinguishes a
// legal unknown-length boundary after the fact via insideUnknownOnly.
func (r *Reader) readFull(buf []byte, id uint32) error {
	n, err := readFullFrom(r.r, buf)
	r.pos += int64(n)
	if err != nil {
		if isEOF(err) {
			return fail(KindTruncated, r.pos, id, "unexpected end of data")
		}
		return err
	}
	return nil
}

// readVINT reads one EBML variable-size integer. If withLength is true the
// VINT_WIDTH marker bits are retained (element IDs need them); otherwise the
// marker is stripped and the numeric value is returned. Unknown-length marker
// (all data bits set) yields the all-ones value that callers map to
// UnknownSize.
func (r *Reader) readVINT(withLength bool) (value uint64, width int, err error) {
	start := r.pos
	var head [1]byte
	if err = r.readFull(head[:], 0); err != nil {
		return 0, 0, err
	}
	mask := byte(0x80)
	width = 1
	for head[0]&mask == 0 {
		mask >>= 1
		width++
		if width > 8 {
			return 0, 0, fail(KindBadVINT, start, 0, "VINT width exceeds 8 bytes")
		}
	}
	raw := make([]byte, width)
	raw[0] = head[0]
	if width > 1 {
		if err = r.readFull(raw[1:], 0); err != nil {
			return 0, 0, err
		}
	}
	var v uint64
	for _, b := range raw {
		v = v<<8 | uint64(b)
	}
	if withLength {
		return v, width, nil
	}
	// Strip the length marker bit and test for the all-ones unknown marker.
	strip := v ^ (uint64(1) << uint(8*width-width))
	allOnes := (uint64(1) << uint(8*width-width)) - 1
	if strip == allOnes {
		return allOnes, width, nil // all-ones is the unknown-length marker
	}
	return strip, width, nil
}

// isUnknownMarker reports whether v is an all-ones VINT payload, which EBML
// reserves as the unknown-length marker for VINT widths 1..4.
func isUnknownMarker(v uint64) bool {
	switch v {
	case (uint64(1) << 7) - 1, (uint64(1) << 14) - 1,
		(uint64(1) << 21) - 1, (uint64(1) << 28) - 1:
		return true
	}
	return false
}

// Uint reads an unsigned integer payload (EBML uint element semantics).
func Uint(data []byte) (uint64, error) {
	if len(data) > 8 {
		return 0, fail(KindOversize, 0, 0, "uint payload too wide: %d bytes", len(data))
	}
	var v uint64
	for _, b := range data {
		v = v<<8 | uint64(b)
	}
	return v, nil
}

// Float reads a 0/4/8-byte EBML float payload.
func Float(data []byte) (float64, error) {
	switch len(data) {
	case 0:
		return 0, nil
	case 4:
		return float64(bigEndianUint32(data)), nil
	case 8:
		return math.Float64frombits(bigEndianUint64(data)), nil
	default:
		return 0, fail(KindBadVINT, 0, 0, "bad float width %d", len(data))
	}
}

func min(a, b int64) int64 {
	if a < b {
		return a
	}
	return b
}
