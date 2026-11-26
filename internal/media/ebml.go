package media

import (
	"fmt"
	"os"
)

// Limits bounds resource use for untrusted local Matroska/WebM-like input.
type Limits struct {
	MaxElementSize int64 `json:"max_element_size"`
	MaxTotalBytes  int64 `json:"max_total_bytes"`
}

// DefaultLimits returns conservative local-processing budgets.
func DefaultLimits() Limits {
	return Limits{MaxElementSize: 16 << 20, MaxTotalBytes: 64 << 20}
}

// Element is one parsed EBML element. Size is -1 for an unknown-length
// element; End is always the resolved exclusive byte boundary.
type Element struct {
	ID           uint32     `json:"id"`
	Name         string     `json:"name"`
	Path         string     `json:"path"`
	HeaderOffset int64      `json:"header_offset"`
	HeaderSize   int        `json:"header_size"`
	DataOffset   int64      `json:"data_offset"`
	Size         int64      `json:"size"`
	UnknownSize  bool       `json:"unknown_size"`
	End          int64      `json:"end"`
	Children     []*Element `json:"-"`
}

// Document contains parsed EBML elements and later extracted media blocks.
type Document struct {
	Elements []*Element `json:"-"`
	All      []*Element `json:"elements"`
	Blocks   []*Block   `json:"blocks,omitempty"`
	Limits   Limits     `json:"limits"`
}

// VINT is a decoded EBML variable-length integer.
type VINT struct {
	Raw     uint64
	Value   uint64
	Width   int
	Unknown bool
}

// ReadVINT reads one VINT starting at off. end is the exclusive legal
// boundary imposed by the parent element.
func ReadVINT(data []byte, off, end int64, path string) (VINT, int64, error) {
	if off < 0 || off >= end || off >= int64(len(data)) {
		return VINT{}, off, NewError(StageEBML, off, path, ErrTruncatedHeader).WithKind("vint_first_byte")
	}
	first := data[off]
	if first == 0 {
		return VINT{}, off, NewError(StageEBML, off, path, ErrInvalidVINT).WithKind("zero_first_byte")
	}

	width := 1
	for width <= 8 && first&(0x80>>(width-1)) == 0 {
		width++
	}
	if width > 8 {
		return VINT{}, off, NewError(StageEBML, off, path, ErrInvalidVINT).WithKind("more_than_eight_bytes")
	}
	next := off + int64(width)
	if next > end || next > int64(len(data)) {
		return VINT{}, off, NewError(StageEBML, off, path, ErrTruncatedHeader).
			WithKind(fmt.Sprintf("vint_needs_%d_bytes", width))
	}

	markerBit := uint64(1) << uint(8-width)
	raw := uint64(first)
	value := uint64(first) & (markerBit - 1)
	for i := 1; i < width; i++ {
		b := data[off+int64(i)]
		raw = (raw << 8) | uint64(b)
		value = (value << 8) | uint64(b)
	}
	valueBits := uint(7 * width)
	unknownMask := uint64(1<<valueBits) - 1
	return VINT{Raw: raw, Value: value, Width: width, Unknown: value == unknownMask}, next, nil
}

// ReadFileLimited reads a local file while enforcing the total memory budget.
func ReadFileLimited(path string, limits Limits) ([]byte, error) {
	info, err := os.Stat(path)
	if err != nil {
		return nil, NewError(StageEBML, 0, path, err).WithKind("stat")
	}
	if info.Size() > limits.MaxTotalBytes {
		return nil, NewError(StageEBML, 0, path, ErrMemoryBudget).WithKind("total_file_size")
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, NewError(StageEBML, 0, path, err).WithKind("read")
	}
	if int64(len(data)) > limits.MaxTotalBytes {
		return nil, NewError(StageEBML, 0, path, ErrMemoryBudget).WithKind("total_read_size")
	}
	return data, nil
}

// ParseEBML parses a complete EBML byte range. A non-nil partial Document may
// accompany an error, but callers must not treat its media blocks as valid.
func ParseEBML(data []byte, limits Limits) (*Document, error) {
	if limits.MaxTotalBytes <= 0 {
		limits = DefaultLimits()
	}
	doc := &Document{Limits: limits}
	if int64(len(data)) > limits.MaxTotalBytes {
		return doc, NewError(StageEBML, 0, "", ErrMemoryBudget).WithKind("input_buffer")
	}
	children, err := parseRange(data, 0, int64(len(data)), "", limits, doc)
	doc.Elements = children
	return doc, err
}

func parseRange(data []byte, start, end int64, parentPath string, limits Limits, doc *Document) ([]*Element, error) {
	var out []*Element
	pos := start
	for pos < end {
		idVINT, afterID, err := ReadVINT(data, pos, end, parentPath)
		if err != nil {
			return out, err
		}
		id := uint32(idVINT.Raw)
		name := EBMLName(id)
		elementPath := parentPath + "/" + name

		sizeVINT, dataOff, err := ReadVINT(data, afterID, end, elementPath)
		if err != nil {
			return out, err
		}
		el := &Element{
			ID:           id,
			Name:         name,
			Path:         elementPath,
			HeaderOffset: pos,
			HeaderSize:   int(dataOff - pos),
			DataOffset:   dataOff,
		}

		if sizeVINT.Unknown {
			el.UnknownSize = true
			el.Size = -1
			el.End = end
		} else {
			el.Size = int64(sizeVINT.Value)
			if el.Size > limits.MaxElementSize {
				return out, NewError(StageEBML, pos, elementPath, ErrMemoryBudget).
					WithKind("element_size_limit")
			}
			elementEnd, addErr := CheckedAdd(dataOff, el.Size)
			if addErr != nil || elementEnd > end || elementEnd > int64(len(data)) {
				return out, NewError(StageEBML, pos, elementPath, ErrLengthOutOfBounds).
					WithKind("declared_size_past_parent_or_file")
			}
			el.End = elementEnd
		}

		doc.All = append(doc.All, el)
		out = append(out, el)

		if IsContainerID(id) {
			children, parseErr := parseRange(data, el.DataOffset, el.End, elementPath, limits, doc)
			el.Children = children
			if parseErr != nil {
				return out, parseErr
			}
		}
		pos = el.End
	}
	if pos != end {
		return out, NewError(StageEBML, pos, parentPath, ErrIllegalBoundary).
			WithKind("range_end_mismatch")
	}
	return out, nil
}

// FindDescendants returns all parsed elements with id, in file order.
func (d *Document) FindDescendants(id uint32) []*Element {
	var found []*Element
	for _, el := range d.All {
		if el.ID == id {
			found = append(found, el)
		}
	}
	return found
}

// FirstChild returns the first direct child with id.
func (e *Element) FirstChild(id uint32) *Element {
	for _, child := range e.Children {
		if child.ID == id {
			return child
		}
	}
	return nil
}
