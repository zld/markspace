package demux

import (
	"encoding/binary"
	"fmt"

	"mediapipeline/internal/fault"
)

// rawBox is one parsed ISO BMFF box with absolute input offsets.
type rawBox struct {
	typ      string
	off      int64
	size     int64
	header   int64
	data     []byte
	children []*rawBox
}

func (b *rawBox) payloadOff() int64 { return b.off + b.header }
func (b *rawBox) payload() []byte   { return b.data[b.off+b.header : b.off+b.size] }

func (b *rawBox) find(typ string) *rawBox {
	for _, c := range b.children {
		if c.typ == typ {
			return c
		}
	}
	return nil
}

func (b *rawBox) findAll(typ string) []*rawBox {
	out := make([]*rawBox, 0, 2)
	for _, c := range b.children {
		if c.typ == typ {
			out = append(out, c)
		}
	}
	return out
}

// path walks child boxes by type.
func (b *rawBox) path(types ...string) *rawBox {
	cur := b
	for _, t := range types {
		if cur == nil {
			return nil
		}
		cur = cur.find(t)
	}
	return cur
}

func containerExtra(typ string) int64 {
	switch typ {
	case "stsd":
		return 8
	case "meta":
		return 4
	case "dref":
		return 8
	case "avc1", "hvc1", "hev1", "encv":
		return 78 // 8-byte SampleEntry + 70 bytes of VisualSampleEntry
	case "mp4a":
		return 28 // 8-byte SampleEntry + 20 bytes of AudioSampleEntry
	}
	return 0
}

func isContainer(typ string) bool {
	switch typ {
	case "moov", "trak", "mdia", "minf", "stbl", "moof", "traf", "mvex",
		"edts", "dinf", "udta", "stsd", "meta", "dref", "mfra",
		"avc1", "hvc1", "hev1", "encv", "mp4a":
		return true
	}
	return false
}

func boxFault(op fault.Operation, offset int64, format string, reason string) error {
	return fault.New(fault.StageDemux, format, op, offset, reason)
}

// parseBoxes parses boxes in [start,end).  When tolerant is true and the last
// top-level box is cut off, parsing stops and the cut range is returned
// instead of an error.  A cut range starts at the first input byte that the
// declared structure needs but the input does not contain.
func parseBoxes(data []byte, start, end int64, format string, tolerant bool) ([]*rawBox, []Range, error) {
	var out []*rawBox
	pos := start
	for pos < end {
		if end-pos < 8 {
			if tolerant {
				return out, []Range{{Start: end, End: pos + 8}}, nil
			}
			return nil, nil, boxFault(fault.OpBoxHeader, end, format, "incomplete box header")
		}
		size32 := int64(binary.BigEndian.Uint32(data[pos:]))
		typ := string(data[pos+4 : pos+8])
		header := int64(8)
		size := size32
		switch size32 {
		case 0:
			size = end - pos
		case 1:
			if end-pos < 16 {
				if tolerant {
					return out, []Range{{Start: end, End: pos + 16}}, nil
				}
				return nil, nil, boxFault(fault.OpBoxHeader, end, format, "incomplete large box header")
			}
			size = int64(binary.BigEndian.Uint64(data[pos+8:]))
			header = 16
		}
		if size < header {
			return nil, nil, boxFault(fault.OpBoxHeader, pos, format, fmt.Sprintf("invalid box size %d for %q", size, typ))
		}
		boxEnd := pos + size
		if boxEnd > end {
			if tolerant {
				return out, []Range{{Start: end, End: boxEnd}}, nil
			}
			return nil, nil, boxFault(fault.OpBoxOverrun, end, format,
				fmt.Sprintf("box %q declares end %d beyond input end %d", typ, boxEnd, end))
		}
		b := &rawBox{typ: typ, off: pos, size: size, header: header, data: data}
		if isContainer(typ) {
			childStart := b.payloadOff() + containerExtra(typ)
			if childStart > boxEnd {
				return nil, nil, boxFault(fault.OpBoxHeader, boxEnd, format, "container header exceeds box")
			}
			children, _, err := parseBoxes(data, childStart, boxEnd, format, false)
			if err != nil {
				return nil, nil, err
			}
			b.children = children
		}
		out = append(out, b)
		pos = boxEnd
	}
	return out, nil, nil
}
