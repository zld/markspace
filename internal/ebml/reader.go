// Package ebml reads Matroska/WebM EBML elements from in-memory byte slices.
// It never reads past the declared bounds of an element or of the source
// buffer: out-of-range lengths are reported with structured errors instead.
package ebml

import (
	"errors"
	"math"
)

// Matroska/WebM element identifiers used across the parser.
const (
	IDEbmlHeader = 0x1A45DFA3
	IDSegment    = 0x18538067
	IDInfo       = 0x1549A966
	IDTracks     = 0x1654AE6B
	IDTrackEntry = 0xAE
	IDCluster    = 0x1F43B675
	IDTimecode   = 0xE7
	IDSimpleBlock = 0xA3
	IDBlockGroup = 0xA0
	IDBlock      = 0xA1
	IDTrackNumber = 0xD7
	IDCodecID     = 0x86
	IDTrackType   = 0x83
	IDDefaultDuration = 0x23E383
	IDTimecodeScale = 0x2AD7B1
	IDDuration   = 0x4489
	IDCues       = 0x1C53BB6B
)

// UnknownSize marks an element whose length is encoded as all-ones VINT
// (unknown length, e.g. a streaming Segment or Cluster).
const UnknownSize int64 = -1

// Error categories. They are stable strings so downstream stages and tests
// can classify failures without parsing human-readable messages.
const (
	CategoryTruncated  = "truncated_input"
	CategoryBadVINT    = "malformed_vint"
	CategoryOutOfRange = "length_out_of_range"
	CategoryBadBlock   = "malformed_block"
)

// Error is a structured parse error carrying the byte offset at which it was
// detected and a machine-readable category.
type Error struct {
	Offset   int64
	Category string
	Message  string
	// Remaining is the number of bytes actually available when a declared
	// size exceeded the region; zero for other categories.
	Remaining int64
	// ContextID is the ID of the master element being parsed when the error
	// was detected (e.g. a Cluster whose child ran past the buffer).
	ContextID uint32
	// ContextOffset is the absolute start offset of that master element.
	ContextOffset int64
}

func (e *Error) Error() string {
	return "ebml: " + e.Category + " at offset " + itoa(e.Offset) + ": " + e.Message
}

func itoa(n int64) string {
	if n == 0 {
		return "0"
	}
	neg := n < 0
	if neg {
		n = -n
	}
	var buf [20]byte
	i := len(buf)
	for n > 0 {
		i--
		buf[i] = byte('0' + n%10)
		n /= 10
	}
	if neg {
		i--
		buf[i] = '-'
	}
	return string(buf[i:])
}

// ErrTruncated is returned when the source ends before an element completes.
var ErrTruncated = errors.New("ebml: truncated input")

// EOFAt is an ErrTruncated wrapper carrying the absolute offset where the
// region ran out of bytes. errors.Is(err, ErrTruncated) still matches.
type EOFAt struct {
	Offset int64
}

func (e *EOFAt) Error() string { return "ebml: truncated input at offset " + itoa(e.Offset) }
func (e *EOFAt) Unwrap() error { return ErrTruncated }

// Element is a parsed EBML element header plus a slice of its payload bytes.
// For master elements Payload is the bounded region containing the children.
type Element struct {
	ID       uint32
	Offset   int64 // offset of the ID byte within the source
	Size     int64 // declared payload size, or UnknownSize
	Header   int   // number of bytes occupied by ID + size VINTs
	Payload  []byte
	End      int64 // exclusive end offset; source length when size is unknown
}

// Reader walks sibling elements inside a bounded byte region.
type Reader struct {
	data    []byte
	pos     int64
	end     int64
	base    int64   // offset of data[0] within the original file
	masters []masterFrame
	lastOK  uint32  // ID of the most recently fully returned element
	lastOff int64   // absolute offset of that element
}

type masterFrame struct {
	id  uint32
	off int64
}

// NewReader creates a reader over data. base is the absolute file offset at
// which data begins so reported offsets remain file-relative.
func NewReader(data []byte, base int64) *Reader {
	return &Reader{data: data, pos: 0, end: int64(len(data)), base: base}
}

// NewReaderContext is NewReader plus an outer master element frame, used
// when descending into a known master without Enter (e.g. salvage paths).
func NewReaderContext(data []byte, base int64, context uint32) *Reader {
	r := NewReader(data, base)
	if context != 0 {
		r.masters = []masterFrame{{id: context, off: base}}
	}
	return r
}

// ContextID returns the innermost master element the reader is inside.
func (r *Reader) ContextID() uint32 {
	if n := len(r.masters); n > 0 {
		return r.masters[n-1].id
	}
	return 0
}

// Enter records el as the master whose payload the caller is about to walk.
func (r *Reader) Enter(el *Element) {
	r.masters = append(r.masters, masterFrame{id: el.ID, off: el.Offset})
}

// Leave pops the most recent Enter frame.
func (r *Reader) Leave() {
	if n := len(r.masters); n > 0 {
		r.masters = r.masters[:n-1]
	}
}

// Done reports whether the region has been fully consumed.
func (r *Reader) Done() bool { return r.pos >= r.end }

// Pos returns the current absolute file offset.
func (r *Reader) Pos() int64 { return r.base + r.pos }

func isUnknown(size uint64, width int) bool {
	// All size bits set to one encodes unknown length.
	return width > 0 && size == (uint64(1)<<(7*width))-1
}

// Next reads one sibling element header and returns it without descending.
// A truncated element at the region boundary returns a structured error.
func (r *Reader) Next() (*Element, error) {
	if r.pos >= r.end {
		return nil, &EOFAt{Offset: r.base + r.pos}
	}
	idStart := r.pos
	id, idWidth, err := readID(r.data, r.pos, r.end)
	if err != nil {
		return nil, r.annotate(err)
	}
	if idWidth == 0 || id == 0 {
		return nil, r.annotate(&Error{Offset: r.base + idStart, Category: CategoryBadVINT,
			Message: "invalid element id"})
	}
	r.pos += int64(idWidth)
	sizeRaw, sizeWidth, err := readVINT(r.data, r.pos, r.end)
	if err != nil {
		return nil, r.annotate(err)
	}
	r.pos += int64(sizeWidth)
	header := idWidth + sizeWidth
	el := &Element{ID: uint32(id), Offset: r.base + idStart, Header: header}
	if isUnknown(sizeRaw, sizeWidth) {
		el.Size = UnknownSize
		el.Payload = r.data[r.pos:r.end]
		el.End = r.base + r.end
		r.lastOK, r.lastOff = el.ID, el.Offset
		// An unknown-length master is parsed in place: its children follow
		// on this same reader. Push it so later errors carry its context.
		r.masters = append(r.masters, masterFrame{id: el.ID, off: el.Offset})
		return el, nil
	}
	if int64(sizeRaw) > r.end-r.pos {
		return nil, r.annotate(&Error{
			Offset:    r.base + idStart,
			Category:  CategoryOutOfRange,
			Message:   "declared size " + itoa(int64(sizeRaw)) + " exceeds remaining region",
			Remaining: r.end - r.pos,
		})
	}
	el.Size = int64(sizeRaw)
	el.Payload = r.data[r.pos : r.pos+int64(sizeRaw)]
	el.End = r.base + r.pos + int64(sizeRaw)
	r.pos += int64(sizeRaw)
	r.lastOK, r.lastOff = el.ID, el.Offset
	return el, nil
}

// LastOK returns the ID and absolute offset of the most recent element that
// Next returned without error.
func (r *Reader) LastOK() (uint32, int64) {
	return r.lastOK, r.lastOff
}

// annotate stamps the current master context onto a structured parse error.
func (r *Reader) annotate(err error) error {
	if pe, ok := err.(*Error); ok && pe.ContextID == 0 && len(r.masters) > 0 {
		top := r.masters[len(r.masters)-1]
		pe.ContextID = top.id
		pe.ContextOffset = top.off
	}
	return err
}

// readID parses an EBML element ID. Unlike a data VINT, an ID retains every
// bit including the leading length marker (e.g. the byte 0xA3 is ID 0xA3).
func readID(data []byte, pos, end int64) (uint64, int, error) {
	if pos >= end {
		return 0, 0, &Error{Offset: pos, Category: CategoryTruncated,
			Message: "no byte for ID marker"}
	}
	first := data[pos]
	width := 0
	for b := uint(7); b > 0; b-- {
		if first&(1<<b) != 0 {
			width = 8 - int(b)
			break
		}
	}
	if width == 0 {
		return 0, 0, &Error{Offset: pos, Category: CategoryBadVINT,
			Message: "ID marker bit missing"}
	}
	if pos+int64(width) > end {
		return 0, 0, &Error{Offset: pos, Category: CategoryTruncated,
			Message: "ID spans past region end"}
	}
	var v uint64
	for i := 0; i < width; i++ {
		v = v<<8 | uint64(data[pos+int64(i)])
	}
	return v, width, nil
}

// readVINT parses an EBML variable-length integer. The leading marker bit
// position determines the width; the remaining bits are the value.
func readVINT(data []byte, pos, end int64) (uint64, int, error) {
	if pos >= end {
		return 0, 0, &Error{Offset: pos, Category: CategoryTruncated,
			Message: "no byte for VINT marker"}
	}
	first := data[pos]
	width := 0
	for b := uint(7); b > 0; b-- {
		if first&(1<<b) != 0 {
			width = 8 - int(b)
			break
		}
	}
	if width == 0 {
		return 0, 0, &Error{Offset: pos, Category: CategoryBadVINT,
			Message: "VINT marker bit missing"}
	}
	if pos+int64(width) > end {
		return 0, 0, &Error{Offset: pos, Category: CategoryTruncated,
			Message: "VINT spans past region end"}
	}
	var v uint64
	for i := 0; i < width; i++ {
		b := data[pos+int64(i)]
		if i == 0 {
			b &= byte(0xFF >> uint(width)) // strip marker bit
		}
		v = v<<8 | uint64(b)
	}
	return v, width, nil
}

// Walk invokes fn for every sibling element until the region is exhausted.
// If fn returns an error the walk stops and that error is returned; parse
// errors from the stream are returned directly.
func Walk(data []byte, base int64, fn func(*Element) error) error {
	r := NewReader(data, base)
	for !r.Done() {
		el, err := r.Next()
		if err != nil {
			if errors.Is(err, ErrTruncated) {
				return err
			}
			return err
		}
		if err := fn(el); err != nil {
			return err
		}
	}
	return nil
}

// PartialElement rebuilds the element that caused an out-of-range error,
// bounded by the bytes actually present. It lets callers salvage confirmed
// children from a truncated master element without ever reading past the
// buffer. The returned element reports UnknownSize.
func PartialElement(data []byte, base int64, pe *Error) (*Element, bool) {
	rel := pe.Offset - base
	if rel < 0 || rel >= int64(len(data)) {
		return nil, false
	}
	id, idWidth, err := readID(data, rel, int64(len(data)))
	if err != nil || idWidth == 0 {
		return nil, false
	}
	pos := rel + int64(idWidth)
	if _, sw, err := readVINT(data, pos, int64(len(data))); err != nil {
		return nil, false
	} else {
		pos += int64(sw)
	}
	return &Element{
		ID:      uint32(id),
		Offset:  pe.Offset,
		Size:    UnknownSize,
		Header:  int(pos - rel),
		Payload: data[pos:],
		End:     base + int64(len(data)),
	}, true
}

// childFor returns a reader for el's payload that inherits parent's stack.
// el is expected to be the element most recently returned by parent.
func (r *Reader) childFor(el *Element) *Reader {
	cr := NewReader(el.Payload, el.Offset+int64(el.Header))
	cr.masters = append(cr.masters, r.masters...)
	cr.Enter(el)
	return cr
}

// ChildReader returns a Reader positioned over the element payload.
func ChildReader(el *Element) *Reader {
	return NewReader(el.Payload, el.Offset+int64(el.Header))
}

// ChildReaderFor returns a context-aware reader over the element payload,
// inheriting the given master stack and pushing el as the new frame.
func ChildReaderFor(parent *Reader, el *Element) *Reader {
	return parent.childFor(el)
}

// Uint parses an EBML unsigned-integer payload (big-endian, max 8 bytes).
func Uint(el *Element) (uint64, bool) {
	if len(el.Payload) == 0 || len(el.Payload) > 8 {
		return 0, false
	}
	var v uint64
	for _, b := range el.Payload {
		v = v<<8 | uint64(b)
	}
	return v, true
}

// Float parses a 4- or 8-byte big-endian IEEE-754 payload.
func Float(el *Element) (float64, bool) {
	switch len(el.Payload) {
	case 4:
		bits := uint32(el.Payload[0])<<24 | uint32(el.Payload[1])<<16 |
			uint32(el.Payload[2])<<8 | uint32(el.Payload[3])
		return float64(math.Float32frombits(bits)), true
	case 8:
		bits := uint64(el.Payload[0])<<56 | uint64(el.Payload[1])<<48 |
			uint64(el.Payload[2])<<40 | uint64(el.Payload[3])<<32 |
			uint64(el.Payload[4])<<24 | uint64(el.Payload[5])<<16 |
			uint64(el.Payload[6])<<8 | uint64(el.Payload[7])
		return math.Float64frombits(bits), true
	default:
		return 0, false
	}
}
