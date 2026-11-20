// Package avc parses length-prefixed AVC (H.264) samples and classifies
// the frame type of each sample.
package avc

import (
	"errors"
	"fmt"
)

var ErrCorrupt = errors.New("avc: corrupt sample")

// FrameType names the picture type of a sample.
type FrameType string

const (
	IDR     FrameType = "IDR"
	I       FrameType = "I"
	P       FrameType = "P"
	B       FrameType = "B"
	Unknown FrameType = "UNKNOWN"
)

// Classification describes one decoded sample.
type Classification struct {
	Type       FrameType
	Slices     int  // number of VCL NAL units seen
	MultiSlice bool // more than one slice in the access unit
}

// ParseAvcC extracts the NAL length field size from an avcC payload.
// Returns a value in {1,2,4}.
func ParseAvcC(avcC []byte) (int, error) {
	if len(avcC) < 7 {
		return 0, fmt.Errorf("%w: avcC too short (%d bytes)", ErrCorrupt, len(avcC))
	}
	if avcC[0] != 1 {
		return 0, fmt.Errorf("%w: avcC configurationVersion %d", ErrCorrupt, avcC[0])
	}
	n := int(avcC[4]&0x03) + 1
	if n == 3 {
		return 0, fmt.Errorf("%w: avcC reserved lengthSizeMinusOne", ErrCorrupt)
	}
	return n, nil
}

// stripEPB removes emulation prevention bytes (00 00 03 -> 00 00).
func stripEPB(in []byte) []byte {
	out := make([]byte, 0, len(in))
	zeros := 0
	for _, b := range in {
		if zeros >= 2 && b == 0x03 {
			zeros = 0
			continue
		}
		if b == 0 {
			zeros++
		} else {
			zeros = 0
		}
		out = append(out, b)
	}
	return out
}

type bitReader struct {
	data []byte
	pos  int // bit position
}

func (br *bitReader) bit() (uint32, error) {
	if br.pos >= len(br.data)*8 {
		return 0, fmt.Errorf("%w: slice header overruns NAL", ErrCorrupt)
	}
	b := br.data[br.pos/8] >> (7 - uint(br.pos%8)) & 1
	br.pos++
	return uint32(b), nil
}

// ue decodes one unsigned Exp-Golomb code.
func (br *bitReader) ue() (uint32, error) {
	zeros := 0
	for {
		b, err := br.bit()
		if err != nil {
			return 0, err
		}
		if b == 1 {
			break
		}
		zeros++
		if zeros > 31 {
			return 0, fmt.Errorf("%w: Exp-Golomb code too long", ErrCorrupt)
		}
	}
	v := uint32(1)<<uint(zeros) - 1
	for i := 0; i < zeros; i++ {
		b, err := br.bit()
		if err != nil {
			return 0, err
		}
		v += b << uint(zeros-1-i)
	}
	return v, nil
}

// sliceFrameType maps slice_type (mod 5) to a frame type.
func sliceFrameType(st uint32) FrameType {
	switch st % 5 {
	case 0, 3: // P, SP
		return P
	case 1: // B
		return B
	case 2, 4: // I, SI
		return I
	}
	return Unknown
}

// Classify inspects one length-prefixed AVC sample. lengthSize is the NAL
// length field width from avcC. VCL NALs are examined; the first slice
// header determines the frame type unless an IDR slice is present.
func Classify(sample []byte, lengthSize int) (Classification, error) {
	if lengthSize < 1 || lengthSize > 4 || lengthSize == 3 {
		return Classification{}, fmt.Errorf("%w: bad length size %d", ErrCorrupt, lengthSize)
	}
	var c Classification
	off := 0
	for off < len(sample) {
		if len(sample)-off < lengthSize {
			return c, fmt.Errorf("%w: truncated NAL length at %d", ErrCorrupt, off)
		}
		var n int
		for i := 0; i < lengthSize; i++ {
			n = n<<8 | int(sample[off+i])
		}
		off += lengthSize
		if n <= 0 || n > len(sample)-off {
			return c, fmt.Errorf("%w: NAL of %d bytes at %d overruns sample of %d",
				ErrCorrupt, n, off, len(sample))
		}
		nal := sample[off : off+n]
		off += n
		nalType := nal[0] & 0x1F
		if nalType < 1 || nalType > 5 {
			continue // non-VCL
		}
		c.Slices++
		if nalType == 5 {
			c.Type = IDR
			continue
		}
		if c.Type == IDR {
			continue
		}
		if c.Slices > 1 {
			continue // type decided by the first slice
		}
		rbsp := stripEPB(nal[1:])
		br := &bitReader{data: rbsp}
		if _, err := br.ue(); err != nil { // first_mb_in_slice
			return c, err
		}
		st, err := br.ue() // slice_type
		if err != nil {
			return c, err
		}
		c.Type = sliceFrameType(st)
	}
	c.MultiSlice = c.Slices > 1
	if c.Slices == 0 {
		c.Type = Unknown
	}
	return c, nil
}
