// Package ebml provides a bounded, streaming reader for EBML element trees
// (the encoding used by Matroska/WebM). It never decodes media payloads and
// reports the absolute file offset of every malformed byte.
package ebml

import (
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"math/bits"
)

// ErrorClass categorises an EBML decoding failure so callers can fail closed
// with a stable, auditable reason.
type ErrorClass string

const (
	ErrBadWidth     ErrorClass = "BAD_VINT_WIDTH"
	ErrTruncated    ErrorClass = "TRUNCATED"
	ErrOversized    ErrorClass = "OVERSIZED"
	ErrNestedTooDeep ErrorClass = "NESTED_TOO_DEEP"
	ErrBadElement   ErrorClass = "BAD_ELEMENT"
)

// Error carries the absolute byte offset at which a defect was detected, the
// element nesting depth and a machine-readable class.
type Error struct {
	Class  ErrorClass
	Offset int64
	Depth  int
	Msg    string
}

func (e *Error) Error() string {
	return fmt.Sprintf("ebml: %s at offset=%d depth=%d: %s", e.Class, e.Offset, e.Depth, e.Msg)
}

// NewError builds a classified offset error.
func NewError(c ErrorClass, off int64, depth int, format string, a ...any) *Error {
	return &Error{Class: c, Offset: off, Depth: depth, Msg: fmt.Sprintf(format, a...)}
}

// Limits bounds untrusted input: the 8 GiB material ceiling, nested depth and
// the maximum size of any single element payload.
type Limits struct {
	MaxFileSize  int64
	MaxDepth     int
	MaxElementSz uint64
}

// DefaultLimits returns the project limits (8 GiB file, 32 levels, 8 GiB elem).
func DefaultLimits() Limits {
	return Limits{MaxFileSize: 8 << 30, MaxDepth: 32, MaxElementSz: 8 << 30}
}

// Element is one parsed EBML element header plus its bounds relative to the
// underlying reader. UnknownSize is true for an unknown-length element; such
// elements are terminated by their parent's end or by a known sibling ID.
type Element struct {
	ID          uint32
	IDWidth     int
	Size        uint64
	SizeWidth   int
	UnknownSize bool
	HeaderOff   int64 // offset of the first ID byte
	DataOff     int64 // offset of the first payload byte
	EndOff      int64 // payload end; -1 when UnknownSize
	Depth       int
}

// Reader scans EBML elements sequentially. It buffers only VINT header bytes,
// never full element payloads, so memory stays independent of file size.
type Reader struct {
	r       io.Reader
	lim     Limits
	off     int64
	rootEnd int64 // -1 until a bounded root is established
}

// NewReader wraps r with the given limits.
func NewReader(r io.Reader, lim Limits) *Reader {
	return &Reader{r: r, lim: lim, rootEnd: -1}
}

// Offset returns the absolute byte position of the reader.
func (r *Reader) Offset() int64 { return r.off }

// ReadByteRaw reads one payload byte without VINT interpretation.
func (r *Reader) ReadByteRaw() (byte, error) { return r.readByte() }

func (r *Reader) readByte() (byte, error) {
	var b [1]byte
	n, err := io.ReadFull(r.r, b[:])
	r.off += int64(n)
	if err != nil {
		if errors.Is(err, io.EOF) || errors.Is(err, io.ErrUnexpectedEOF) {
			return 0, NewError(ErrTruncated, r.off, 0, "expected byte, got EOF")
		}
		return 0, err
	}
	return b[0], nil
}

// ReadVInt reads one EBML variable-length integer. keepAll retains the VINT
// marker (element IDs); data VINTs strip the length bit. It returns the value,
// the number of bytes consumed and whether the reserved all-ones width marker
// (unknown size) was present.
func (r *Reader) ReadVInt(keepAll bool) (val uint64, width int, unknown bool, err error) {
	start := r.off
	first, err := r.readByte()
	if err != nil {
		return 0, 0, false, err
	}
	width = 0
	switch {
	case first&0x80 != 0:
		width = 1
	case first&0x40 != 0:
		width = 2
	case first&0x20 != 0:
		width = 3
	case first&0x10 != 0:
		width = 4
	case first&0x08 != 0:
		width = 5
	case first&0x04 != 0:
		width = 6
	case first&0x02 != 0:
		width = 7
	case first&0x01 != 0:
		width = 8
	default:
		return 0, 0, false, NewError(ErrBadWidth, start, 0, "VINT length descriptor 0x00 is illegal")
	}
	raw := uint64(first)
	for i := 1; i < width; i++ {
		b, e := r.readByte()
		if e != nil {
			return 0, 0, false, NewError(ErrTruncated, e.(*Error).Offset, 0, "VINT body truncated after %d of %d bytes", i, width)
		}
		raw = raw<<8 | uint64(b)
	}
	if keepAll {
		return raw, width, false, nil
	}
	// EBML unknown-length marker is reserved to the 8-byte form:
	// length bit 0x01 followed by 56 one-bits (0x01FFFFFFFFFFFFFF).
	if width == 8 && raw == 0x01FFFFFFFFFFFFFF {
		return 0, width, true, nil
	}
	lengthBit := uint64(1) << uint(8*width-width)
	return raw - lengthBit, width, false, nil
}

// ReadElementHeader parses the next element header at depth. A bounded parent
// end (parentEnd, -1 for unknown) lets the reader detect truncation precisely.
func (r *Reader) ReadElementHeader(depth int, parentEnd int64) (*Element, error) {
	if depth > r.lim.MaxDepth {
		return nil, NewError(ErrNestedTooDeep, r.off, depth, "depth %d exceeds limit %d", depth, r.lim.MaxDepth)
	}
	if parentEnd >= 0 && r.off >= parentEnd {
		return nil, io.EOF
	}
	headerOff := r.off
	id, idW, _, err := r.ReadVInt(true)
	if err != nil {
		return nil, err
	}
	if idW > 4 {
		return nil, NewError(ErrBadWidth, headerOff, depth, "element ID width %d exceeds 4 octets", idW)
	}
	size, sizeW, unknown, err := r.ReadVInt(false)
	if err != nil {
		return nil, err
	}
	if !unknown && size > r.lim.MaxElementSz {
		return nil, NewError(ErrOversized, r.off, depth, "element 0x%X payload %d bytes exceeds limit %d", id, size, r.lim.MaxElementSz)
	}
	e := &Element{
		ID: uint32(id), IDWidth: idW, Size: size, SizeWidth: sizeW,
		UnknownSize: unknown, HeaderOff: headerOff, DataOff: r.off, EndOff: -1, Depth: depth,
	}
	if !unknown {
		e.EndOff = r.off + int64(size)
		if r.lim.MaxFileSize > 0 && e.EndOff > r.lim.MaxFileSize {
			return nil, NewError(ErrOversized, headerOff, depth, "element 0x%X ends at %d beyond file limit", id, e.EndOff)
		}
		if parentEnd >= 0 && e.EndOff > parentEnd {
			return nil, NewError(ErrTruncated, headerOff, depth, "element 0x%X payload overruns parent by %d bytes", id, e.EndOff-parentEnd)
		}
	}
	return e, nil
}

// ChildIterator yields the direct children of a (possibly unknown-length)
// parent. For unknown-length parents, iteration stops at stopID or the parent
// boundary reported by the enclosing iterator; bounded parents stop at EndOff.
type ChildIterator struct {
	r        *Reader
	parent   *Element
	parentEnd int64 // hard boundary even for unknown parents
	stopIDs  map[uint32]bool
}

// Children starts iteration over the children of e. hardEnd is the enclosing
// known boundary (or -1); stopID lists sibling IDs that terminate an unknown
// length parent.
func (r *Reader) Children(e *Element, hardEnd int64, stopIDs ...uint32) *ChildIterator {
	end := hardEnd
	if !e.UnknownSize {
		end = e.EndOff
	}
	m := make(map[uint32]bool, len(stopIDs))
	for _, id := range stopIDs {
		m[id] = true
	}
	return &ChildIterator{r: r, parent: e, parentEnd: end, stopIDs: m}
}

// Next returns the next child header or io.EOF at the parent boundary.
func (it *ChildIterator) Next() (*Element, error) {
	if it.parentEnd >= 0 && it.r.off >= it.parentEnd {
		return nil, io.EOF
	}
	// Sibling-ID termination only applies to unknown-length parents: bounded
	// parents have an exact byte boundary and must read every declared child.
	if it.parent.UnknownSize && len(it.stopIDs) > 0 && it.r.off > it.parent.DataOff {
		peek, err := it.peekID()
		if err != nil {
			return nil, err
		}
		if it.stopIDs[peek] {
			return nil, io.EOF
		}
	}
	return it.r.ReadElementHeader(it.parent.Depth+1, it.parentEnd)
}

// peekID decodes only the next element ID, then rewinds to the start.
func (it *ChildIterator) peekID() (uint32, error) {
	off := it.r.off
	first, err := it.r.readByte()
	if err != nil {
		return 0, err
	}
	w := bits.LeadingZeros8(first) + 1
	if w > 4 {
		return 0, NewError(ErrBadWidth, off, it.parent.Depth, "illegal element ID descriptor 0x%02X", first)
	}
	id := uint32(first)
	for i := 1; i < w; i++ {
		b, e := it.r.readByte()
		if e != nil {
			return 0, e
		}
		id = id<<8 | uint32(b)
	}
	if err := it.r.SeekTo(off); err != nil {
		return 0, err
	}
	return id, nil
}

// Discard skips n bytes (used to skip payloads without buffering them).
func (r *Reader) Discard(n int64) (int64, error) {
	if n < 0 {
		return 0, NewError(ErrBadElement, r.off, 0, "negative discard %d", n)
	}
	got, err := io.CopyN(io.Discard, r.r, n)
	r.off += got
	if err != nil {
		if errors.Is(err, io.EOF) || errors.Is(err, io.ErrUnexpectedEOF) {
			return got, NewError(ErrTruncated, r.off, 0, "payload truncated: wanted %d more bytes", n-got)
		}
		return got, err
	}
	return got, nil
}

// Seek moves the absolute read position; the underlying reader must support it.
func (r *Reader) SeekTo(off int64) error {
	s, ok := r.r.(io.Seeker)
	if !ok {
		return NewError(ErrBadElement, r.off, 0, "underlying reader is not seekable")
	}
	p, err := s.Seek(off, io.SeekStart)
	if err != nil {
		return err
	}
	r.off = p
	return nil
}

// ReadUint reads a big-endian unsigned integer payload of 1..8 bytes.
func (r *Reader) ReadUint(n int) (uint64, error) {
	if n < 0 || n > 8 {
		return 0, NewError(ErrBadElement, r.off, 0, "uint width %d out of range", n)
	}
	buf := make([]byte, n)
	if _, err := io.ReadFull(r.r, buf); err != nil {
		r.off += int64(len(buf))
		return 0, NewError(ErrTruncated, r.off, 0, "uint payload truncated")
	}
	r.off += int64(n)
	var v uint64
	for _, b := range buf {
		v = v<<8 | uint64(b)
	}
	return v, nil
}

// ReadBytes reads exactly n payload bytes into a freshly allocated slice.
func (r *Reader) ReadBytes(n int) ([]byte, error) {
	buf := make([]byte, n)
	got, err := io.ReadFull(r.r, buf)
	r.off += int64(got)
	if err != nil {
		return nil, NewError(ErrTruncated, r.off, 0, "byte payload truncated after %d of %d", got, n)
	}
	return buf, nil
}

// SkipElement discards the full payload of a bounded element.
func (r *Reader) SkipElement(e *Element) error {
	if e.UnknownSize {
		return NewError(ErrBadElement, e.HeaderOff, e.Depth, "cannot skip unknown-length element 0x%X", e.ID)
	}
	_, err := r.Discard(e.EndOff - r.off)
	return err
}

// UintSize is a convenience for fixed-size unsigned element values.
func UintSize(b []byte) uint64 { return binary.BigEndian.Uint64(append(make([]byte, 8-len(b)), b...)) }
