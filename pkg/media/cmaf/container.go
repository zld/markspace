package cmaf

import (
	"encoding/binary"
	"fmt"

	"cmafrepair/pkg/media/timebase"
)

// childBox describes a box found while walking a container payload.
type childBox struct {
	hdr     BoxHeader
	payload []byte
}

// parseContainer walks immediate children of a container payload. off is the
// absolute file offset at which payload starts, so child offsets stay
// absolute. It never reads beyond the supplied payload.
func parseContainer(payload []byte, off int64) ([]childBox, error) {
	var out []childBox
	pos := int64(0)
	for pos < int64(len(payload)) {
		if int64(len(payload))-pos < 8 {
			return nil, timebase.AtError(timebase.CodeReadOutOfBounds, timebase.PhaseScan, off+pos,
				"container has %d trailing bytes", int64(len(payload))-pos)
		}
		rawSize := int64(binary.BigEndian.Uint32(payload[pos : pos+4]))
		typ := string(payload[pos+4 : pos+8])
		hdr := BoxHeader{Type: typ, Offset: off + pos, HeaderSize: Size32}
		bodyStart := pos + 8
		switch rawSize {
		case 0:
			hdr.Size = -1
			hdr.PayloadOff = off + bodyStart
			hdr.PayloadLen = -1
			rawSize = int64(len(payload)) - pos
		case 1:
			if int64(len(payload))-pos < 16 {
				return nil, timebase.AtError(timebase.CodeReadOutOfBounds, timebase.PhaseScan, off+pos,
					"truncated largesize child box")
			}
			large := int64(binary.BigEndian.Uint64(payload[pos+8 : pos+16]))
			hdr.HeaderSize = Size64
			hdr.Size = large
			hdr.PayloadOff = off + pos + 16
			hdr.PayloadLen = large - 16
			rawSize = large
		default:
			hdr.Size = rawSize
			hdr.PayloadOff = off + bodyStart
			hdr.PayloadLen = rawSize - 8
		}
		if rawSize < hdr.HeaderSize || pos+rawSize > int64(len(payload)) {
			return nil, timebase.AtError(timebase.CodeBoxLength, timebase.PhaseScan, hdr.Offset,
				"child box %s length %d exceeds container", typ, rawSize)
		}
		var body []byte
		if hdr.PayloadLen > 0 {
			body = payload[bodyStart : pos+rawSize]
		}
		out = append(out, childBox{hdr: hdr, payload: body})
		pos += rawSize
	}
	return out, nil
}

// findChild returns the first child of the given type, or nil.
func findChild(children []childBox, typ string) *childBox {
	for i := range children {
		if children[i].hdr.Type == typ {
			return &children[i]
		}
	}
	return nil
}

// String renders a header for diagnostics.
func (b BoxHeader) String() string {
	return fmt.Sprintf("%s@%d(size=%d)", b.Type, b.Offset, b.Size)
}
