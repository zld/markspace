// Package ebml parses Matroska/WebM EBML structure with strict bounds
// checking, stable error codes, and byte-offset localization.
package ebml

import (
	"fmt"

	"mediad/internal/clock"
)

// Element IDs (marker bits retained, as stored).
const (
	IDEBML               = 0x1A45DFA3
	IDSegment            = 0x18538067
	IDSeekHead           = 0x114D9B74
	IDInfo               = 0x1549A966
	IDTimecodeScale      = 0x2AD7B1
	IDTracks             = 0x1654AE6B
	IDTrackEntry         = 0xAE
	IDTrackNumber        = 0xD7
	IDTrackTimecodeScale = 0x23314F
	IDCluster            = 0x1F43B675
	IDTimecode           = 0xE7
	IDSimpleBlock        = 0xA3
	IDBlockGroup         = 0xA0
	IDBlock              = 0xA1
	IDBlockDuration      = 0x9B
	IDCues               = 0x1C53BB6B
)

// UnknownSize marks an element whose size VINT is all ones.
const UnknownSize = -1

// DefaultMaxElementSize caps any single element payload (64 MiB).
const DefaultMaxElementSize = 1 << 26

const maxDepth = 32

var masters = map[uint32]bool{
	IDSegment: true, IDInfo: true, IDTracks: true,
	IDTrackEntry: true, IDCluster: true, IDBlockGroup: true,
}

// clusterSiblings are segment-level IDs that legally terminate an
// unknown-size Cluster (a parent boundary, not a child).
var clusterSiblings = map[uint32]bool{
	IDCluster: true, IDInfo: true, IDTracks: true,
	IDCues: true, IDSeekHead: true,
}

// Element is one parsed EBML element header.
type Element struct {
	ID         uint32
	Offset     int64 // offset of the element ID in the file
	HeaderSize int64 // ID + size field length
	Size       int64 // payload size, UnknownSize if unknown
	Path       string
}

// End returns the offset just past the payload, or -1 if unknown.
func (e Element) End() int64 {
	if e.Size == UnknownSize {
		return -1
	}
	return e.Offset + e.HeaderSize + e.Size
}

// Node is an element plus, for master elements, its children. Leaf payloads
// are retained in Data (bounded by MaxElementSize).
type Node struct {
	Element
	Children []*Node
	Data     []byte
}

// Parser walks a whole in-memory buffer. Callers are expected to have
// applied their own file-size memory cap before loading.
type Parser struct {
	data           []byte
	MaxElementSize int64
}

// NewParser wraps data with the default element-size cap.
func NewParser(data []byte) *Parser {
	return &Parser{data: data, MaxElementSize: DefaultMaxElementSize}
}

// Parse parses all top-level elements.
func (p *Parser) Parse() ([]*Node, error) {
	nodes, _, err := p.parseElements(0, int64(len(p.data)), "", nil, 0)
	return nodes, err
}

// parseElements parses elements in [pos, end). stopIDs, when non-nil, holds
// IDs that belong to a parent level: encountering one ends an unknown-size
// master without consuming it. Returns the nodes and the stop position.
func (p *Parser) parseElements(pos, end int64, path string, stopIDs map[uint32]bool, depth int) ([]*Node, int64, error) {
	if depth > maxDepth {
		return nil, pos, clock.At(clock.ErrBounds, pos, path, "nesting depth exceeds %d", maxDepth)
	}
	var nodes []*Node
	for pos < end {
		id, idLen, err := p.readID(pos, end, path)
		if err != nil {
			return nil, pos, err
		}
		if stopIDs != nil && stopIDs[id] {
			return nodes, pos, nil
		}
		size, sizeLen, err := p.readSize(pos+int64(idLen), end, path)
		if err != nil {
			return nil, pos, err
		}
		el := Element{
			ID:         id,
			Offset:     pos,
			HeaderSize: int64(idLen + sizeLen),
			Size:       size,
			Path:       path + "/" + Name(id),
		}
		node := &Node{Element: el}
		payloadStart := pos + el.HeaderSize
		if size == UnknownSize {
			if !masters[id] {
				return nil, pos, clock.At(clock.ErrUnknownSizeMisuse, pos, el.Path,
					"unknown size on non-master element")
			}
			var childStop map[uint32]bool
			if id == IDCluster {
				childStop = clusterSiblings
			}
			children, stop, err := p.parseElements(payloadStart, end, el.Path, childStop, depth+1)
			if err != nil {
				return nil, pos, err
			}
			node.Children = children
			pos = stop
		} else {
			payloadEnd := payloadStart + size
			if payloadEnd > end {
				return nil, pos, clock.At(clock.ErrBounds, pos, el.Path,
					"element payload [%d,%d) exceeds parent end %d", payloadStart, payloadEnd, end)
			}
			if size > p.MaxElementSize {
				return nil, pos, clock.At(clock.ErrSizeOverflow, pos, el.Path,
					"element size %d exceeds cap %d", size, p.MaxElementSize)
			}
			if masters[id] {
				children, _, err := p.parseElements(payloadStart, payloadEnd, el.Path, nil, depth+1)
				if err != nil {
					return nil, pos, err
				}
				node.Children = children
			} else {
				node.Data = p.data[payloadStart:payloadEnd]
			}
			pos = payloadEnd
		}
		nodes = append(nodes, node)
	}
	return nodes, pos, nil
}

// readID reads an element ID (VINT with marker retained, 1-4 bytes).
func (p *Parser) readID(pos, end int64, path string) (uint32, int, error) {
	if pos >= end {
		return 0, 0, clock.At(clock.ErrTruncatedHeader, pos, path, "missing element ID")
	}
	b := p.data[pos]
	if b == 0 {
		return 0, 0, clock.At(clock.ErrInvalidVINT, pos, path, "element ID starts with 0x00")
	}
	length := 1
	for mask := byte(0x80); b&mask == 0; mask >>= 1 {
		length++
	}
	if length > 4 {
		return 0, 0, clock.At(clock.ErrInvalidVINT, pos, path, "element ID longer than 4 bytes")
	}
	if pos+int64(length) > end {
		return 0, 0, clock.At(clock.ErrTruncatedHeader, pos, path,
			"element ID needs %d bytes, %d remain", length, end-pos)
	}
	var id uint32
	for i := 0; i < length; i++ {
		id = id<<8 | uint32(p.data[pos+int64(i)])
	}
	return id, length, nil
}

// readSize reads an element size (VINT with marker stripped, 1-8 bytes).
// All-ones yields UnknownSize.
func (p *Parser) readSize(pos, end int64, path string) (int64, int, error) {
	if pos >= end {
		return 0, 0, clock.At(clock.ErrTruncatedHeader, pos, path, "missing element size")
	}
	b := p.data[pos]
	if b == 0 {
		return 0, 0, clock.At(clock.ErrInvalidVINT, pos, path, "element size starts with 0x00")
	}
	length := 1
	for mask := byte(0x80); b&mask == 0; mask >>= 1 {
		length++
	}
	if pos+int64(length) > end {
		return 0, 0, clock.At(clock.ErrTruncatedHeader, pos, path,
			"element size needs %d bytes, %d remain", length, end-pos)
	}
	val := uint64(b & (0xFF >> length))
	for i := 1; i < length; i++ {
		val = val<<8 | uint64(p.data[pos+int64(i)])
	}
	if val == uint64(1)<<(7*length)-1 {
		return UnknownSize, length, nil
	}
	return int64(val), length, nil
}

// Name returns a human-readable element name for paths.
func Name(id uint32) string {
	switch id {
	case IDEBML:
		return "EBML"
	case IDSegment:
		return "Segment"
	case IDSeekHead:
		return "SeekHead"
	case IDInfo:
		return "Info"
	case IDTimecodeScale:
		return "TimecodeScale"
	case IDTracks:
		return "Tracks"
	case IDTrackEntry:
		return "TrackEntry"
	case IDTrackNumber:
		return "TrackNumber"
	case IDTrackTimecodeScale:
		return "TrackTimecodeScale"
	case IDCluster:
		return "Cluster"
	case IDTimecode:
		return "Timecode"
	case IDSimpleBlock:
		return "SimpleBlock"
	case IDBlockGroup:
		return "BlockGroup"
	case IDBlock:
		return "Block"
	case IDBlockDuration:
		return "BlockDuration"
	case IDCues:
		return "Cues"
	default:
		return fmt.Sprintf("0x%X", id)
	}
}

// Uint decodes a big-endian unsigned integer payload.
func (n *Node) Uint() (uint64, error) {
	if len(n.Data) == 0 || len(n.Data) > 8 {
		return 0, clock.At(clock.ErrCorruptBlock, n.Offset, n.Path,
			"uint payload length %d out of range", len(n.Data))
	}
	var v uint64
	for _, b := range n.Data {
		v = v<<8 | uint64(b)
	}
	return v, nil
}

// Find returns the first child with the given ID, or nil.
func (n *Node) Find(id uint32) *Node {
	for _, c := range n.Children {
		if c.ID == id {
			return c
		}
	}
	return nil
}

// FindAll returns all children with the given ID.
func (n *Node) FindAll(id uint32) []*Node {
	var out []*Node
	for _, c := range n.Children {
		if c.ID == id {
			out = append(out, c)
		}
	}
	return out
}
