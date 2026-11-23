// Package ebml implements a streaming reader for the subset of EBML used by
// Matroska/WebM captures. It records the original byte span of every element so
// downstream stages can prove which input bytes produced an output.
package ebml

import (
	"errors"
	"math/bits"
)

var (
	// ErrInvalidVINT reports a variable-length integer without a valid marker.
	ErrInvalidVINT = errors.New("ebml: invalid variable-length integer")
	// ErrTruncatedVINT reports a variable-length integer cut before its end.
	ErrTruncatedVINT = errors.New("ebml: truncated variable-length integer")
)

// EBML width limits from the Matroska/WebM specification.
const (
	MaxIDWidth   = 4
	MaxSizeWidth = 8
)

// SizeVINT is a decoded EBML element data size.
type SizeVINT struct {
	Value   uint64
	Width   int
	Unknown bool
}

// ParseID decodes an EBML element ID. The marker bit is retained in the value,
// as required for EBML IDs. Width is the number of bytes consumed.
func ParseID(buf []byte) (id uint32, width int, err error) {
	if len(buf) == 0 {
		return 0, 0, ErrTruncatedVINT
	}
	if buf[0] == 0 {
		return 0, 0, ErrInvalidVINT
	}
	width = bits.LeadingZeros8(buf[0]) + 1
	if width > MaxIDWidth {
		return 0, 0, ErrInvalidVINT
	}
	if len(buf) < width {
		return 0, 0, ErrTruncatedVINT
	}
	var v uint32
	for i := 0; i < width; i++ {
		v = v<<8 | uint32(buf[i])
	}
	return v, width, nil
}

// ParseSize decodes an EBML element data size. Unknown sizes (every data bit
// set) are reported through Unknown instead of an error.
func ParseSize(buf []byte) (SizeVINT, error) {
	raw, width, unknown, err := parseVINT(buf, MaxSizeWidth)
	if err != nil {
		return SizeVINT{}, err
	}
	return SizeVINT{Value: raw, Width: width, Unknown: unknown}, nil
}

// ParseUintVINT decodes an EBML unsigned integer with the marker bit stripped.
// Track numbers inside blocks use this encoding.
func ParseUintVINT(buf []byte) (value uint64, width int, err error) {
	raw, width, _, err := parseVINT(buf, MaxSizeWidth)
	return raw, width, err
}

func parseVINT(buf []byte, maxWidth int) (value uint64, width int, unknown bool, err error) {
	if len(buf) == 0 {
		return 0, 0, false, ErrTruncatedVINT
	}
	if buf[0] == 0 {
		return 0, 0, false, ErrInvalidVINT
	}
	width = bits.LeadingZeros8(buf[0]) + 1
	if width > maxWidth {
		return 0, 0, false, ErrInvalidVINT
	}
	if len(buf) < width {
		return 0, 0, false, ErrTruncatedVINT
	}
	mask := byte(0xFF >> width)
	value = uint64(buf[0] & mask)
	unknown = value == uint64(mask)
	for i := 1; i < width; i++ {
		if buf[i] != 0xFF {
			unknown = false
		}
		value = value<<8 | uint64(buf[i])
	}
	return value, width, unknown, nil
}

// EncodeSize encodes a data size using the minimal width, or the unknown
// encoding when unknown is true.
func EncodeSize(value uint64, unknown bool) []byte {
	if unknown {
		return []byte{0xFF}
	}
	width := 1
	for ; width < MaxSizeWidth; width++ {
		if value < uint64(1)<<(7*width)-1 {
			break
		}
	}
	return EncodeSizeWidth(value, width, false)
}

// EncodeSizeWidth encodes a data size in exactly width bytes. Width must be in
// [1, 8] and value must fit in 7*width-1 bits (the all-ones pattern at that
// width is reserved for "unknown"). It returns nil otherwise.
func EncodeSizeWidth(value uint64, width int, unknown bool) []byte {
	if width < 1 || width > MaxSizeWidth {
		return nil
	}
	out := make([]byte, width)
	if unknown {
		for i := range out {
			out[i] = 0xFF
		}
		out[0] = byte(0xFF>>width) | byte(1)<<(8-width)
		return out
	}
	if value >= uint64(1)<<(7*width)-1 {
		return nil
	}
	for i := width - 1; i >= 0; i-- {
		out[i] = byte(value)
		value >>= 8
	}
	out[0] |= byte(1) << (8 - width)
	return out
}
