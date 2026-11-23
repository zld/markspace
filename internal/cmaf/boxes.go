// Package cmaf parses and validates CMAF/fMP4 fragments and their
// provenance markers.
package cmaf

import (
	"encoding/binary"
	"fmt"

	"mediapack/internal/clock"
)

// BoxHeader describes one validated top-level box.
type BoxHeader struct {
	Type       string
	Offset     int64
	Size       int64 // total size including header
	HeaderSize int64
}

const (
	smallHeader = 8
	largeHeader = 16
)

// ScanBoxes validates the top-level box structure of data and returns the
// box headers in order. Truncated headers, undersized boxes and boxes
// overrunning the buffer are rejected with their byte offset.
func ScanBoxes(data []byte) ([]BoxHeader, error) {
	var out []BoxHeader
	pos, n := int64(0), int64(len(data))
	for pos < n {
		remain := n - pos
		if remain < smallHeader {
			return nil, &clock.Error{Cat: clock.CatParse, Offset: pos,
				Msg: fmt.Sprintf("truncated box header: %d bytes remain", remain)}
		}
		size := int64(binary.BigEndian.Uint32(data[pos : pos+4]))
		typ := string(data[pos+4 : pos+8])
		hs := int64(smallHeader)
		switch size {
		case 1: // 64-bit largesize
			if remain < largeHeader {
				return nil, &clock.Error{Cat: clock.CatParse, Offset: pos,
					Msg: "truncated largesize box header"}
			}
			size = int64(binary.BigEndian.Uint64(data[pos+8 : pos+16]))
			hs = largeHeader
		case 0: // box extends to end of buffer
			size = remain
		}
		if size < hs {
			return nil, &clock.Error{Cat: clock.CatParse, Offset: pos,
				Msg: fmt.Sprintf("box %q smaller than its header: size=%d", typ, size)}
		}
		if size > remain {
			return nil, &clock.Error{Cat: clock.CatParse, Offset: pos,
				Msg: fmt.Sprintf("box %q overruns buffer: size=%d remain=%d", typ, size, remain)}
		}
		out = append(out, BoxHeader{Type: typ, Offset: pos, Size: size, HeaderSize: hs})
		pos += size
	}
	return out, nil
}
