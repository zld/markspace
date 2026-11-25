// Package probe scans MP4/MOV containers, resolves tracks and recovers sample
// boundaries when the container index is missing or damaged. Every failure is
// pinned to an absolute byte offset in the original input.
package probe

import (
	"io"

	"tracecheck/internal/clock"
)

// MaxAlloc caps any slice grown from an untrusted on-wire count. A count that
// would exceed it is treated as an out-of-bounds structural fault rather than
// an allocation request.
const MaxAlloc = 64 << 20 // 64 MiB of entries is far beyond any real trace

// Node is one parsed box. Children are populated for container boxes.
type Node struct {
	Type       string
	Offset     clock.Offset // absolute header offset in the input
	HeaderSize int64
	Size       int64 // declared total box size
	PayloadOff clock.Offset
	PayloadLen int64
	Children   []*Node
	// Fault is set when the box itself could not be fully parsed; scanning of
	// sibling boxes continues regardless.
	Fault *clock.OffsetError
}

// IsContainer reports whether the box type holds nested boxes.
func IsContainer(t string) bool {
	switch t {
	case "moov", "trak", "mdia", "minf", "stbl", "udta", "edts", "dinf":
		return true
	}
	return false
}

// Scan walks every box reachable from the input. Truncated headers, lengths
// past EOF and unsupported 64-bit sizes each produce a categorised fault whose
// offset is the real header position; the returned tree still contains every
// box that was fully readable.
func Scan(data []byte) ([]*Node, []*clock.OffsetError) {
	root, faults := scanRegion(data, 0, int64(len(data)), 0)
	return root, faults
}

// scanRegion parses boxes inside [start, limit). depth guards recursion.
func scanRegion(data []byte, start, limit int64, depth int) ([]*Node, []*clock.OffsetError) {
	var nodes []*Node
	var faults []*clock.OffsetError
	pos := start
	if depth > 32 {
		return nodes, append(faults, clock.NewOffsetError(clock.Offset(pos),
			clock.CategoryBounds, "box nesting too deep", nil))
	}
	for pos < limit {
		if limit-pos < 8 {
			faults = append(faults, clock.NewOffsetError(clock.Offset(pos),
				clock.CategoryTruncated, "fewer than 8 header bytes remain", nil))
			break
		}
		// go-mp4 is the spec-correct header parser (handles size==0 and the
		// 64-bit largesize form); the bounds decision against this file stays
		// local so every fault keeps its original offset.
		h, hErr := readBoxHeader(data, pos, limit)
		if hErr != nil {
			faults = append(faults, hErr)
			return nodes, faults
		}
		typ := h.boxType
		hdr := h.headerSize
		size := h.size
		if size < hdr {
			faults = append(faults, clock.NewOffsetError(clock.Offset(pos),
				clock.CategoryBounds, "declared box size below its header", nil))
			return nodes, faults
		}

		if size > limit-pos {
			fault := clock.NewOffsetError(clock.Offset(pos),
				clock.CategoryTruncated, "box size "+typ+" exceeds remaining bytes", nil)
			faults = append(faults, fault)
			// Keep the node with a bounded payload so later stages can still
			// recover samples from the bytes that are present.
			size = limit - pos
			nodes = append(nodes, &Node{
				Type:       typ,
				Offset:     clock.Offset(pos),
				HeaderSize: hdr,
				Size:       size,
				PayloadOff: clock.Offset(pos + hdr),
				PayloadLen: size - hdr,
				Fault:      fault,
			})
			return nodes, faults
		}

		n := &Node{
			Type:       typ,
			Offset:     clock.Offset(pos),
			HeaderSize: hdr,
			Size:       size,
			PayloadOff: clock.Offset(pos + hdr),
			PayloadLen: size - hdr,
		}
		nodes = append(nodes, n)

		if IsContainer(typ) {
			childStart := pos + hdr
			childLimit := pos + size
			kids, kidFaults := scanRegion(data, childStart, childLimit, depth+1)
			n.Children = kids
			faults = append(faults, kidFaults...)
		}
		pos += size
	}
	return nodes, faults
}

// Find returns the first node of the given type in a depth-first walk, or nil.
func Find(nodes []*Node, typ string) *Node {
	for _, n := range nodes {
		if n.Type == typ {
			return n
		}
		if found := Find(n.Children, typ); found != nil {
			return found
		}
	}
	return nil
}

// FindAll returns every node of the type in depth-first order.
func FindAll(nodes []*Node, typ string) []*Node {
	var out []*Node
	for _, n := range nodes {
		if n.Type == typ {
			out = append(out, n)
		}
		out = append(out, FindAll(n.Children, typ)...)
	}
	return out
}

// Payload returns a node's payload slice, bounds-checked against the input.
func (n *Node) Payload(data []byte) ([]byte, error) {
	off := int64(n.PayloadOff)
	if _, _, err := clock.Checked(n.PayloadOff, n.PayloadLen, int64(len(data)), n.Type); err != nil {
		return nil, clock.NewOffsetError(n.PayloadOff, clock.CategoryBounds,
			"payload slice outside file", err)
	}
	return data[off : off+n.PayloadLen], nil
}

// guardCount rejects entry counts that cannot safely back an allocation.
func guardCount(n int64, what string, off clock.Offset) error {
	if n < 0 {
		return clock.NewOffsetError(off, clock.CategoryBounds, what+" count negative", nil)
	}
	if n > MaxAlloc/4 {
		return clock.NewOffsetError(off, clock.CategoryBounds, what+" count too large", nil)
	}
	return nil
}

var _ io.Reader // keep io semantics documented for later section readers
