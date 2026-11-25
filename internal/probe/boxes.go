package probe

import (
	"encoding/binary"
	"fmt"
	"io"
	"math"
	"os"

	"mediaflow/internal/fault"
)

type boxNode struct {
	typ          string
	start        int64
	headerSize   int64
	size         int64
	payloadStart int64
	payloadEnd   int64
	children     []*boxNode
}

func (b *boxNode) pathType() string { return b.typ }

var containerTypes = map[string]bool{
	"moov": true, "trak": true, "mdia": true, "minf": true,
	"stbl": true, "edts": true, "dinf": true, "udta": true,
}

// scanBoxTree walks a bounded MP4 box hierarchy. It returns located faults instead
// of guessing a recovery offset after an invalid length.
func scanBoxTree(f *os.File, start, limit int64) ([]*boxNode, []fault.Interval) {
	var nodes []*boxNode
	var intervals []fault.Interval
	cursor := start
	for cursor < limit {
		header := make([]byte, 16)
		n, err := f.ReadAt(header[:8], cursor)
		if err == io.EOF && n == 0 {
			break
		}
		if err != nil && err != io.EOF {
			intervals = append(intervals, byteFault(fault.ClassTruncated, cursor, cursor+int64(n),
				"cannot read box header: %v", err))
			break
		}
		if n < 8 {
			intervals = append(intervals, byteFault(fault.ClassTruncated, cursor, limit,
				"short box header: read %d of 8 bytes", n))
			break
		}
		size32 := binary.BigEndian.Uint32(header[0:4])
		boxType := string(header[4:8])
		headerSize := int64(8)
		var size int64
		switch size32 {
		case 0:
			size = limit - cursor
		case 1:
			if _, err := f.ReadAt(header[8:16], cursor+8); err != nil {
				intervals = append(intervals, byteFault(fault.ClassTruncated, cursor, limit,
					"64-bit box size is truncated for %q", boxType))
				return nodes, intervals
			}
			usize := binary.BigEndian.Uint64(header[8:16])
			if usize > math.MaxInt64 {
				intervals = append(intervals, byteFault(fault.ClassBoxLength, cursor, cursor+16,
					"64-bit box size overflows int64: %d", usize))
				return nodes, intervals
			}
			size = int64(usize)
			headerSize = 16
		default:
			size = int64(size32)
		}

		if size < headerSize {
			in := byteFault(fault.ClassBoxLength, cursor, cursor+8,
				"box %q declared size %d is shorter than header %d", boxType, size, headerSize)
			in.Kind = "invalid_box_length"
			intervals = append(intervals, in)
			return nodes, intervals
		}
		end, overflow := add64(cursor, size)
		if overflow || end > limit {
			in := byteFault(fault.ClassTruncated, cursor, limit,
				"box %q declared end %d exceeds available end %d", boxType, end, limit)
			in.Kind = "truncated_box"
			if overflow {
				in.Class = fault.ClassBoxLength
				in.Reject = true
				in.Quarantine = true
				in.Message = "box end offset overflows; refusing to guess next box"
			}
			intervals = append(intervals, in)
			return nodes, intervals
		}

		node := &boxNode{
			typ: boxType, start: cursor, headerSize: headerSize, size: size,
			payloadStart: cursor + headerSize, payloadEnd: end,
		}
		if containerTypes[boxType] {
			children, childFaults := scanBoxTree(f, node.payloadStart, node.payloadEnd)
			node.children = children
			intervals = append(intervals, childFaults...)
		}
		nodes = append(nodes, node)
		cursor = end
	}
	return nodes, intervals
}

func add64(a, b int64) (int64, bool) {
	if b < 0 || a > math.MaxInt64-b {
		return 0, true
	}
	return a + b, false
}

func min64(a, b int64) int64 {
	if a < b {
		return a
	}
	return b
}

func byteFault(class fault.Class, start, end int64, format string, args ...any) fault.Interval {
	reject := class == fault.ClassBoxLength || class == fault.ClassTruncated
	quarantine := reject
	return fault.Interval{
		Kind: string(class), Class: class, ByteStart: start, ByteEnd: end,
		Reject: reject, Quarantine: quarantine, Message: fmt.Sprintf(format, args...),
	}
}

// payload reads a bounded box payload. Table payloads are intentionally small.
func payload(f *os.File, n *boxNode) ([]byte, error) {
	size := n.payloadEnd - n.payloadStart
	if size > 16*1024*1024 {
		return nil, fault.New(fault.ClassUnsupported, "box %s payload too large for table parse", n.typ)
	}
	b := make([]byte, size)
	if _, err := f.ReadAt(b, n.payloadStart); err != nil {
		return nil, fault.Wrap(fault.ClassTruncated, err, "read box %s payload", n.typ)
	}
	return b, nil
}

func u16(b []byte, off int) uint16 { return binary.BigEndian.Uint16(b[off : off+2]) }
func u32(b []byte, off int) uint32 { return binary.BigEndian.Uint32(b[off : off+4]) }
func u64(b []byte, off int) uint64 { return binary.BigEndian.Uint64(b[off : off+8]) }

func fullBoxVersionFlags(b []byte) (byte, uint32) {
	if len(b) < 4 {
		return 0, 0
	}
	return b[0], uint32(b[1])<<16 | uint32(b[2])<<8 | uint32(b[3])
}

func findChild(n *boxNode, typ string) *boxNode {
	for _, c := range n.children {
		if c.typ == typ {
			return c
		}
	}
	return nil
}

func findChildren(n *boxNode, typ string) []*boxNode {
	var out []*boxNode
	for _, c := range n.children {
		if c.typ == typ {
			out = append(out, c)
		}
	}
	return out
}
