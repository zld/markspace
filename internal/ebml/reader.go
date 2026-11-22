// Package ebml implements a streaming Extensible Binary Meta Language reader
// sized for Matroska/WebM demuxing. It never materializes a whole element:
// element headers are parsed on demand and payloads are streamed, which keeps
// input size bounded independently of file length.
package ebml

import (
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"math"
)

// Hard safety limits. Inputs exceeding any of these are rejected rather than
// trusted, so a corrupt header cannot force unbounded allocation or recursion.
const (
	MaxElementSize = 256 << 20 // 256 MiB per single element payload
	MaxDepth       = 16        // nested element depth
	MaxIDLength    = 4         // Matroska element IDs are at most VINT_WIDTH 4
)

// ErrorKind classifies an EBML failure so callers can distinguish corrupt
// input from internal/resource failures without string matching.
type ErrorKind int

const (
	KindOK        ErrorKind = iota
	KindTruncated           // input ended before an element (header or payload) completed
	KindOversize            // declared size/depth exceeds a configured or hard limit
	KindBadVINT             // malformed variable-size integer
)

// Error carries a machine-readable kind plus the byte offset and, when
// available, the element ID being processed when the failure occurred.
type Error struct {
	Kind   ErrorKind
	Offset int64
	ElemID uint32
	Msg    string
}

func (e *Error) Error() string {
	return fmt.Sprintf("ebml: %s at offset %d (element 0x%X)", e.Msg, e.Offset, e.ElemID)
}

// Is supports errors.Is on Kind, e.g. errors.Is(err, &Error{Kind: KindTruncated}).
func (e *Error) Is(target error) bool {
	t, ok := target.(*Error)
	return ok && t.Kind == e.Kind
}

func fail(kind ErrorKind, offset int64, id uint32, format string, args ...any) error {
	return &Error{Kind: kind, Offset: offset, ElemID: id, Msg: fmt.Sprintf(format, args...)}
}

// frame is one open element on the descent stack. end==-1 marks an
// unknown-length element whose boundary is only known when its parent ends.
type frame struct {
	id  uint32
	end int64 // absolute byte offset of the payload end, or -1
}

// Reader is a streaming, depth-first EBML parser. The typical loop is:
//
//	for {
//	    el, err := r.Next()
//	    if errors.Is(err, io.EOF) { break }
//	    if el.Container { r.Enter() } else { data, _ := r.ReadData(); ... }
//	}
//
// Next returns elements of the currently entered container; Enter/Exit move
// between nesting levels.
type Reader struct {
	r       io.Reader
	pos     int64
	stack   []frame
	current *Element // element most recently returned by Next
}

// Element describes one EBML element header. Payload is not read until the
// caller asks for it via Reader.ReadData or skips it via Reader.Skip.
type Element struct {
	ID        uint32
	Size      int64 // -1 when the element uses the unknown-length marker
	Offset    int64 // header start offset
	DataOff   int64 // payload start offset
	Container bool
}

// NewReader wraps r. The reader does not require io.ReadSeeker: unknown-length
// boundaries are tracked structurally, which is what makes truncated
// unknown-length Segment/Cluster streams diagnosable.
func NewReader(r io.Reader) *Reader {
	return &Reader{r: r}
}

// Position returns the absolute byte offset of the next unread byte.
func (r *Reader) Position() int64 { return r.pos }

// readFull wraps io.ReadFull with position accounting. Any EOF while bytes
// were expected is KindTruncated; the clean EOF path in Next distinguishes a
// legal unknown-length boundary after the fact via insideUnknownOnly.
func (r *Reader) readFull(buf []byte, id uint32) error {
	n, err := io.ReadFull(r.r, buf)
	r.pos += int64(n)
	if err != nil {
		if errors.Is(err, io.ErrUnexpectedEOF) || errors.Is(err, io.EOF) {
			return fail(KindTruncated, r.pos, id, "unexpected end of data")
		}
		return err
	}
	return nil
}

// readVINT reads one EBML variable-size integer. If withLength is true the
// VINT_WIDTH marker bits are retained (element IDs need them); otherwise the
// marker is stripped and the numeric value is returned. Unknown-length marker
// (all data bits set) yields size -1 when withLength is false.
func (r *Reader) readVINT(withLength bool) (value uint64, width int, err error) {
	start := r.pos
	var head [1]byte
	if err = r.readFull(head[:], 0); err != nil {
		return 0, 0, err
	}
	mask := byte(0x80)
	width = 1
	for head[0]&mask == 0 {
		mask >>= 1
		width++
		if width > 8 {
			return 0, 0, fail(KindBadVINT, start, 0, "VINT width exceeds 8 bytes")
		}
	}
	raw := make([]byte, width)
	raw[0] = head[0]
	if width > 1 {
		if err = r.readFull(raw[1:], 0); err != nil {
			return 0, 0, err
		}
	}
	var v uint64
	for _, b := range raw {
		v = v<<8 | uint64(b)
	}
	if withLength {
		return v, width, nil
	}
	// Strip the length marker bit and test for the all-ones unknown marker.
	strip := v ^ (uint64(1) << uint(8*width-width))
	allOnes := (uint64(1) << uint(8*width-width)) - 1
	if strip == allOnes {
		return allOnes, width, nil // all-ones is the unknown-length marker
	}
	return strip, width, nil
}

// UnknownSize is the sentinel returned for elements encoded with the EBML
// unknown-length marker.
const UnknownSize = int64(-1)

// isUnknownMarker reports whether v is an all-ones VINT payload, which EBML
// reserves as the unknown-length marker for VINT widths 1..4.
func isUnknownMarker(v uint64) bool {
	switch v {
	case (uint64(1) << 7) - 1, (uint64(1) << 14) - 1,
		(uint64(1) << 21) - 1, (uint64(1) << 28) - 1:
		return true
	}
	return false
}

// insideUnknownOnly reports that every open frame is unknown-length, so the
// physical end of the stream is also the container's legal boundary.
func (r *Reader) insideUnknownOnly() bool {
	for _, f := range r.stack {
		if f.end >= 0 {
			return false
		}
	}
	return len(r.stack) > 0
}

// parentEnd returns the payload end of the innermost frame with a known
// length, or -1 when every open frame is unknown-length.
func (r *Reader) parentEnd() int64 {
	for i := len(r.stack) - 1; i >= 0; i-- {
		if r.stack[i].end >= 0 {
			return r.stack[i].end
		}
	}
	return -1
}

// atContainerEnd reports whether the current container's payload is exhausted.
// Unknown-length containers end only when the enclosing known-length parent
// (or the physical stream) ends.
func (r *Reader) atContainerEnd() bool {
	if len(r.stack) == 0 {
		return false
	}
	top := r.stack[len(r.stack)-1]
	if top.end >= 0 {
		return r.pos >= top.end
	}
	if pe := r.parentEndAt(len(r.stack) - 1); pe >= 0 {
		return r.pos >= pe
	}
	return false
}

// parentEndAt scans below frame idx for the nearest known-length boundary.
func (r *Reader) parentEndAt(idx int) int64 {
	for i := idx - 1; i >= 0; i-- {
		if r.stack[i].end >= 0 {
			return r.stack[i].end
		}
	}
	return -1
}

// Next returns the next element header inside the currently entered container.
// io.EOF is returned at a clean container/stream boundary; a *Error of
// KindTruncated is returned when the stream dies mid-header or mid-payload.
func (r *Reader) Next() (*Element, error) {
	if r.atContainerEnd() {
		return nil, io.EOF
	}
	start := r.pos
	idRaw, idWidth, err := r.readVINT(true)
	if err != nil {
		// Physical EOF while positioned at a clean unknown-length container
		// boundary is a normal end, not truncation: no parent promised more
		// bytes than the stream delivered.
		var ee *Error
		if errors.As(err, &ee) && ee.Kind == KindTruncated {
			// Clean EOF only at the top level with zero bytes read, or at the
			// boundary of an unknown-length container that promised no size.
			if (len(r.stack) == 0 && r.pos == start) || r.insideUnknownOnly() {
				return nil, io.EOF
			}
		}
		return nil, err
	}
	if idWidth > MaxIDLength {
		return nil, fail(KindBadVINT, start, 0, "element ID too wide")
	}
	id := uint32(idRaw)
	sizeRaw, _, err := r.readVINT(false)
	if err != nil {
		// Return a partial element so callers retain the offset and ID for
		// truncated-element diagnostics even when the size VINT is cut.
		return &Element{ID: id, Size: UnknownSize, Offset: start, DataOff: r.pos}, err
	}
	size := int64(sizeRaw)
	if isUnknownMarker(sizeRaw) {
		size = UnknownSize
	}
	if size != UnknownSize {
		if size > MaxElementSize {
			return nil, fail(KindOversize, start, id, "element size %d exceeds limit %d", size, MaxElementSize)
		}
		if pe := r.parentEnd(); pe >= 0 && r.pos+size > pe {
			return nil, fail(KindTruncated, start, id,
				"element payload overruns parent boundary: end %d > %d", r.pos+size, pe)
		}
	}
	if len(r.stack) >= MaxDepth {
		return nil, fail(KindOversize, start, id, "element depth exceeds %d", MaxDepth)
	}
	r.current = &Element{
		ID:        id,
		Size:      size,
		Offset:    start,
		DataOff:   r.pos,
		Container: isContainerID(id),
	}
	return r.current, nil
}

// Enter descends into the container element most recently returned by Next.
// The element's header must already have been consumed (it is, by Next).
func (r *Reader) Enter(el *Element) error {
	if !el.Container {
		return fail(KindBadVINT, el.Offset, el.ID, "element is not a container")
	}
	end := int64(-1)
	if el.Size != UnknownSize {
		end = el.DataOff + el.Size
	}
	r.stack = append(r.stack, frame{id: el.ID, end: end})
	r.current = nil
	return nil
}

// Skip discards the payload of the element returned by Next. For an
// unknown-length element inside a known-length parent, skipping consumes up
// to the parent boundary; otherwise only zero-byte payloads are skippable.
func (r *Reader) Skip(el *Element) error {
	r.current = nil
	if el.Size == UnknownSize {
		return fail(KindBadVINT, el.Offset, el.ID, "cannot skip unknown-length element")
	}
	remaining := el.DataOff + el.Size - r.pos
	if remaining < 0 {
		return fail(KindTruncated, el.Offset, el.ID, "reader already past element end")
	}
	discard := make([]byte, min(64*1024, remaining))
	for remaining > 0 {
		n := int64(len(discard))
		if n > remaining {
			n = remaining
		}
		if err := r.readFull(discard[:n], el.ID); err != nil {
			return err
		}
		remaining -= n
	}
	return nil
}

// ReadData returns the full payload of a known-length scalar element. The
// size is bounded by MaxElementSize, so this cannot allocate unboundedly.
func (r *Reader) ReadData(el *Element) ([]byte, error) {
	r.current = nil
	if el.Size == UnknownSize {
		return nil, fail(KindBadVINT, el.Offset, el.ID, "cannot read unknown-length scalar")
	}
	remaining := el.DataOff + el.Size - r.pos
	if remaining < 0 {
		return nil, fail(KindTruncated, el.Offset, el.ID, "reader already past element end")
	}
	buf := make([]byte, remaining)
	if err := r.readFull(buf, el.ID); err != nil {
		return nil, err
	}
	return buf, nil
}

// Exit closes the current container, consuming any unread payload up to its
// known boundary. Unknown-length containers simply pop the frame.
func (r *Reader) Exit() error {
	if len(r.stack) == 0 {
		return fail(KindBadVINT, r.pos, 0, "Exit without Enter")
	}
	top := r.stack[len(r.stack)-1]
	if top.end >= 0 && r.pos < top.end {
		if err := r.Skip(&Element{ID: top.id, Size: top.end - r.pos, Offset: r.pos, DataOff: r.pos}); err != nil {
			return err
		}
	}
	r.stack = r.stack[:len(r.stack)-1]
	r.current = nil
	return nil
}

// Uint reads an unsigned integer payload (EBML uint element semantics).
func Uint(data []byte) (uint64, error) {
	if len(data) > 8 {
		return 0, fail(KindOversize, 0, 0, "uint payload too wide: %d bytes", len(data))
	}
	var v uint64
	for _, b := range data {
		v = v<<8 | uint64(b)
	}
	return v, nil
}

// Float reads a 0/4/8-byte EBML float payload.
func Float(data []byte) (float64, error) {
	switch len(data) {
	case 0:
		return 0, nil
	case 4:
		return float64(binary.BigEndian.Uint32(data)), nil
	case 8:
		return math.Float64frombits(binary.BigEndian.Uint64(data)), nil
	default:
		return 0, fail(KindBadVINT, 0, 0, "bad float width %d", len(data))
	}
}

func min(a, b int64) int64 {
	if a < b {
		return a
	}
	return b
}
