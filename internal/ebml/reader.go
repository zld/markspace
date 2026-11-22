// Package ebml implements a streaming Extensible Binary Meta Language reader
// sized for Matroska/WebM demuxing. It never materializes a whole element:
// element headers are parsed on demand and payloads are streamed, which keeps
// input size bounded independently of file length.
package ebml

import (
	"errors"
	"io"
)

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
