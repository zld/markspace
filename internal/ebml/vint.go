package ebml

import (
	"fmt"

	"tracecheck/internal/serr"
)

// MaxVINTWidth is the largest legal EBML variable-length integer width.
const MaxVINTWidth = 8

// VINTWidth returns the byte width implied by the first byte of a VINT:
// the number of leading zero bits plus one. A zero first byte would require
// more than eight bytes and is therefore illegal.
func VINTWidth(first byte) (int, bool) {
	if first == 0 {
		return 0, false
	}
	w := 1
	for mask := byte(0x80); first&mask == 0; mask >>= 1 {
		w++
	}
	return w, true
}

// DecodeVINT decodes one VINT from the start of buf. It returns the decoded
// value, its byte width and whether the value is the reserved all-ones
// "unknown" marker. Errors carry no offset; callers annotate them with the
// input position at which the VINT started.
func DecodeVINT(buf []byte) (val uint64, width int, unknown bool, err *serr.Error) {
	if len(buf) == 0 {
		return 0, 0, false, serr.New(serr.CodeTruncatedHeader, "empty VINT")
	}
	width, ok := VINTWidth(buf[0])
	if !ok {
		return 0, 0, false, serr.New(serr.CodeIllegalVINT, "VINT wider than %d bytes", MaxVINTWidth)
	}
	if len(buf) < width {
		return 0, width, false, serr.New(serr.CodeTruncatedHeader, "VINT needs %d bytes, have %d", width, len(buf))
	}
	// Data bits of the first byte: everything below the length marker.
	v := uint64(buf[0]) & (uint64(1)<<(8-uint(width)) - 1)
	allOnes := v == (uint64(1)<<(8-uint(width))-1)
	for i := 1; i < width; i++ {
		v = (v << 8) | uint64(buf[i])
		if buf[i] != 0xFF {
			allOnes = false
		}
	}
	return v, width, allOnes, nil
}

// VINTLength returns the minimal byte width able to encode value as a VINT.
// A width-N VINT carries 7*N data bits; the all-ones pattern is reserved for
// the unknown-length marker, so the largest encodable value is 2^(7N)-2.
func VINTLength(value uint64) int {
	for w := 1; w <= MaxVINTWidth; w++ {
		// value+1 leaves room below the reserved all-ones pattern.
		if value+1 < (uint64(1) << (7 * uint(w))) {
			return w
		}
	}
	return -1 // value does not fit even at width 8
}

// EncodeVINT encodes value using its minimal legal width.
func EncodeVINT(value uint64) ([]byte, error) {
	w := VINTLength(value)
	if w < 0 {
		return nil, serr.New(serr.CodeIllegalVINT, "value %d does not fit in a VINT", value)
	}
	out := make([]byte, w)
	// Place the data bits across the width, then set the length marker.
	v := value
	for i := w - 1; i >= 0; i-- {
		out[i] = byte(v & 0xFF)
		v >>= 8
	}
	out[0] |= byte(1 << (8 - uint(w)))
	return out, nil
}

// EncodeUnknownVINT returns the reserved all-ones VINT of the given width,
// which encodes an unknown element length.
func EncodeUnknownVINT(width int) ([]byte, error) {
	if width < 1 || width > MaxVINTWidth {
		return nil, serr.New(serr.CodeIllegalVINT, "unknown VINT width %d out of range", width)
	}
	out := make([]byte, width)
	for i := range out {
		out[i] = 0xFF
	}
	// First byte: marker bit plus all available data bits set — 0xFF already is.
	if width == 1 {
		return out, nil
	}
	return out, nil
}

// MustEncodeVINT panics on unencodable values; intended for test fixtures.
func MustEncodeVINT(value uint64) []byte {
	b, err := EncodeVINT(value)
	if err != nil {
		panic(fmt.Sprintf("MustEncodeVINT(%d): %v", value, err))
	}
	return b
}
