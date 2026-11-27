// Package ebml implements a streaming EBML reader for the Matroska/WebM
// subset used by this project. It records byte offsets and element paths on
// every error and understands unknown-length (live) elements: an
// unknown-length element is legal only as the final child of its parent (or
// at document root), and otherwise terminates at the first enclosing
// boundary or at end of file.
package ebml

import (
	"bytes"
	"encoding/binary"
	"io"
	"math"

	"github.com/local/tracecheck/internal/clock"
)

// Element describes one parsed EBML element header together with its
// position in the source stream.
type Element struct {
	ID         uint32
	IDLen      int
	Size       int64 // -1 means unknown length
	SizeLen    int
	Offset     int64 // offset of the ID in the source
	HeaderLen  int   // ID VINT + size VINT
	DataOffset int64 // first payload byte
	Parent     *Element
}

// EndOffset returns the offset just past the element payload, or -1 when
// the element has unknown length.
func (e *Element) EndOffset() int64 {
	if e.Size < 0 {
		return -1
	}
	return e.DataOffset + e.Size
}

// Path renders the element chain as /ID/ID using hexadecimal IDs.
func (e *Element) Path() string {
	if e.Parent != nil {
		return e.Parent.Path() + "/" + idHex(e.ID)
	}
	return "/" + idHex(e.ID)
}

// Reader parses EBML elements from a seekable byte stream. The end field
// is an absolute byte offset (not a length): sub-readers share the same
// coordinate system as their parent, which keeps boundary checks correct
// inside nested and unknown-length elements.
type Reader struct {
	r       io.ReadSeeker
	end     int64 // absolute end offset of the readable region, -1 if open
	unknown bool  // region has no resolvable end
}

// NewReader wraps a seekable stream. end is the absolute byte offset beyond
// which data must not be read; pass -1 for an open-ended stream.
func NewReader(r io.ReadSeeker, end int64) *Reader {
	return &Reader{r: r, end: end, unknown: end < 0}
}

// readVINT reads one EBML variable-length integer. Element IDs must not use
// the reserved all-ones width marker (e.g. 0xFF); size VINTs may use it to
// signal unknown length, hence allowReserved.
func (rd *Reader) readVINT(maxLen int, allowReserved bool) (value uint64, width int, raw []byte, err error) {
	head := make([]byte, 1)
	if _, err = io.ReadFull(rd.r, head); err != nil {
		return 0, 0, nil, err
	}
	b0 := head[0]
	if b0 == 0 {
		return 0, 0, head, clock.NewError(clock.CodeIllegalVINT, 0, "", "VINT length marker is zero")
	}
	width = 1
	for width < maxLen && b0&(0x80>>(width-1)) == 0 {
		width++
	}
	if b0&(0x80>>(width-1)) == 0 {
		return 0, width, head, clock.NewError(clock.CodeIllegalVINT, 0, "", "VINT wider than allowed")
	}
	rest := make([]byte, width-1)
	if _, err = io.ReadFull(rd.r, rest); err != nil {
		return 0, width, head, err
	}
	raw = append(head, rest...)
	// Reserved all-ones VINT: the length marker's data bits and every
	// following byte are all one, e.g. FF, 7FFF, 3FFFFF.
	if b0 == (0xFF>>(width-1))&0xFF {
		allOnes := true
		for _, b := range rest {
			if b != 0xFF {
				allOnes = false
				break
			}
		}
		if allOnes && !allowReserved {
			return 0, width, raw, clock.NewError(clock.CodeIllegalVINT, 0, "", "reserved all-ones VINT")
		}
	}
	value = uint64(b0 & (0x7F >> (width - 1)))
	for _, b := range rest {
		value = (value << 8) | uint64(b)
	}
	return value, width, raw, nil
}

func idHex(id uint32) string {
	const hexd = "0123456789ABCDEF"
	out := make([]byte, 0, 8)
	started := false
	for shift := 24; shift >= 0; shift -= 8 {
		b := byte(id >> shift)
		if b != 0 || started {
			started = true
			out = append(out, hexd[b>>4], hexd[b&0x0F])
		}
	}
	if len(out) == 0 {
		return "00"
	}
	return string(out)
}

// ReadElementHeader parses one element header at the current position. The
// parent chain (or nil at root) is attached so callers can render paths and
// compute boundaries.
func (rd *Reader) ReadElementHeader(parent *Element) (*Element, error) {
	start, err := rd.r.Seek(0, io.SeekCurrent)
	if err != nil {
		return nil, err
	}
	// Element IDs retain their VINT length-marker bits: the canonical
	// Matroska IDs (e.g. Segment = 0x18538067) include the marker, so the
	// ID value is assembled from the raw VINT bytes.
	idVal, idLen, idRaw, err := rd.readVINT(4, false)
	if err != nil {
		if te := clock.AsTraceError(err); te != nil {
			return nil, te.AtOffset(start).AtPath(parentPath(parent))
		}
		return nil, err
	}
	var idFull uint32
	for _, b := range idRaw {
		idFull = (idFull << 8) | uint32(b)
	}
	_ = idVal
	sizeVal, sizeLen, _, err := rd.readVINT(8, true)
	if err != nil {
		if te := clock.AsTraceError(err); te != nil {
			return nil, te.AtOffset(start).AtPath(parentPath(parent))
		}
		if err == io.EOF || err == io.ErrUnexpectedEOF {
			return nil, clock.NewError(clock.CodeTruncatedElement, start, parentPath(parent), "size VINT truncated")
		}
		return nil, err
	}
	el := &Element{
		ID:         idFull,
		IDLen:      idLen,
		Size:       int64(sizeVal),
		SizeLen:    sizeLen,
		Offset:     start,
		HeaderLen:  idLen + sizeLen,
		DataOffset: start + int64(idLen+sizeLen),
		Parent:     parent,
	}
	// Unknown-length size: every data bit of the size VINT is one
	// (e.g. FF for width 1, 7FFF for width 2).
	if sizeVal == unknownMarker(sizeLen) {
		el.Size = -1
	}
	if err := rd.checkBounds(el, parent); err != nil {
		return nil, err
	}
	return el, nil
}

func unknownMarker(width int) uint64 {
	// A size VINT of this width carries 7*width data bits; unknown length is
	// signalled when all data bits are one (0x7F for width 1, 0x3FFF for 2).
	bits := 7 * width
	if bits >= 64 {
		return ^uint64(0)
	}
	return (uint64(1) << uint(bits)) - 1
}

func parentPath(p *Element) string {
	if p == nil {
		return "/"
	}
	return p.Path()
}

func (rd *Reader) checkBounds(el, parent *Element) error {
	if el.Size < 0 {
		// Unknown length is legal only at root or as last child; the caller
		// validates the "last child" rule when its sibling ends. We still
		// verify that a known-length parent can contain the header.
		if parent != nil && parent.Size >= 0 && el.DataOffset > parent.EndOffset() {
			return clock.NewError(clock.CodeBoundary, el.Offset, parent.Path(),
				"unknown-length element starts beyond parent end")
		}
		return nil
	}
	end := el.DataOffset + el.Size
	if !rd.unknown && end > rd.end {
		return clock.NewError(clock.CodeTruncatedElement, el.Offset, parentPath(parent),
			"element payload extends past end of file")
	}
	if parent != nil && parent.Size >= 0 && end > parent.EndOffset() {
		return clock.NewError(clock.CodeTruncatedElement, el.Offset, parent.Path(),
			"element payload extends past parent boundary")
	}
	return nil
}

// Skip positions the stream just past el, honoring unknown length by
// seeking to the enclosing boundary (parent end, or EOF).
func (rd *Reader) Skip(el *Element) error {
	if el.Size >= 0 {
		_, err := rd.r.Seek(el.EndOffset(), io.SeekStart)
		return err
	}
	target := int64(0)
	if el.Parent != nil && el.Parent.Size >= 0 {
		target = el.Parent.EndOffset()
	} else if rd.end >= 0 {
		target = rd.end
	} else {
		return clock.NewError(clock.CodeBoundary, el.Offset, el.Path(),
			"cannot skip unknown-length element without boundary")
	}
	_, err := rd.r.Seek(target, io.SeekStart)
	return err
}

// ReadPayload reads the full element payload into memory.
func (rd *Reader) ReadPayload(el *Element) ([]byte, error) {
	if el.Size < 0 {
		return nil, clock.NewError(clock.CodeBoundary, el.Offset, el.Path(),
			"cannot materialize unknown-length payload")
	}
	if _, err := rd.r.Seek(el.DataOffset, io.SeekStart); err != nil {
		return nil, err
	}
	buf := make([]byte, el.Size)
	if _, err := io.ReadFull(rd.r, buf); err != nil {
		return nil, clock.NewError(clock.CodeTruncatedElement, el.DataOffset, el.Path(),
			"payload bytes missing: "+err.Error())
	}
	return buf, nil
}

// ChildReader returns a Reader scoped to el's payload and a closer that
// restores the outer stream position. Unknown-length children inherit the
// nearest enclosing known boundary.
func (rd *Reader) ChildReader(el *Element) (*Reader, int64, error) {
	if _, err := rd.r.Seek(el.DataOffset, io.SeekStart); err != nil {
		return nil, 0, err
	}
	size := el.Size
	end := int64(0)
	if size < 0 {
		if el.Parent != nil && el.Parent.Size >= 0 {
			end = el.Parent.EndOffset()
		} else if rd.end >= 0 {
			end = rd.end
		}
	} else {
		end = el.DataOffset + size
	}
	if end < el.DataOffset {
		return nil, 0, clock.NewError(clock.CodeBoundary, el.Offset, el.Path(),
			"unknown-length element has no resolvable boundary")
	}
	return NewReader(rd.r, end), end, nil
}

// Uint decodes an EBML unsigned integer payload (big endian, max 8 bytes).
func Uint(data []byte) (uint64, error) {
	if len(data) > 8 {
		return 0, clock.NewError(clock.CodeOutOfRange, 0, "", "uint payload exceeds 8 bytes")
	}
	var v uint64
	for _, b := range data {
		v = (v << 8) | uint64(b)
	}
	return v, nil
}

// SInt decodes an EBML signed integer payload (two's complement).
func SInt(data []byte) (int64, error) {
	if len(data) == 0 || len(data) > 8 {
		return 0, clock.NewError(clock.CodeOutOfRange, 0, "", "sint payload size invalid")
	}
	var u uint64
	for _, b := range data {
		u = (u << 8) | uint64(b)
	}
	if data[0]&0x80 != 0 {
		// Sign-extend the unused high bits.
		u |= ^uint64(0) << (8 * len(data))
	}
	return int64(u), nil
}

// Float decodes an EBML float payload (4 or 8 bytes, big endian IEEE 754).
func Float(data []byte) (float64, error) {
	switch len(data) {
	case 4:
		bits := binary.BigEndian.Uint32(data)
		return float64(floatFromBits32(bits)), nil
	case 8:
		return floatFromBits64(binary.BigEndian.Uint64(data)), nil
	default:
		return 0, clock.NewError(clock.CodeOutOfRange, 0, "", "float payload must be 4 or 8 bytes")
	}
}

// NewBytesReader builds a Reader over an in-memory buffer.
func NewBytesReader(data []byte) *Reader {
	return NewReader(bytes.NewReader(data), int64(len(data)))
}

func floatFromBits32(b uint32) float32 { return math.Float32frombits(b) }
func floatFromBits64(b uint64) float64 { return math.Float64frombits(b) }
