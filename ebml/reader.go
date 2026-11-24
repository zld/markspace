// Package ebml implements a streaming Matroska-flavoured EBML reader.
//
// The reader never buffers whole elements: containers are walked with an
// explicit depth stack and unknown-length (live) elements are supported via
// stop-ID peeking. All sizes are bounded and every truncated read is reported
// with its byte offset.
package ebml

import (
	"io"

	"mediacheck/timeline"
)

const (
	// MaxElementPayload bounds a single known-size element payload (256 MiB).
	MaxElementPayload int64 = 256 << 20
	// MaxDepth bounds container nesting; deeper streams are rejected.
	MaxDepth = 16
	// UnknownSize marks an EBML element whose VINT size is all ones.
	UnknownSize int64 = -1
)

// ElementID is a raw EBML element identifier (including its VINT prefix).
type ElementID uint64

// Header describes one EBML element as found in the stream.
type Header struct {
	ID         ElementID
	Size       int64 // payload length, UnknownSize for live elements
	Offset     int64 // absolute offset of the first ID byte
	DataOffset int64 // absolute offset where the payload starts
	HeaderLen  int   // ID + size VINT length in bytes
}

// End returns the absolute end offset for known-size elements, or -1.
func (h *Header) End() int64 {
	if h.Size == UnknownSize {
		return UnknownSize
	}
	return h.DataOffset + h.Size
}

type frame struct {
	end  int64 // absolute container end, UnknownSize when live
	stop map[ElementID]bool
}

// ReadSeekerAt is the stream contract the streaming reader requires.
type ReadSeekerAt interface {
	io.Reader
	io.ReaderAt
	io.Seeker
}

// Reader streams EBML elements from a seekable source.
type Reader struct {
	r     ReadSeekerAt
	stack []frame
}

// NewReader wraps an existing seekable stream.
func NewReader(r ReadSeekerAt) *Reader { return &Reader{r: r} }

// Depth returns the current container nesting depth.
func (r *Reader) Depth() int { return len(r.stack) }

// Pos returns the current absolute byte offset.
func (r *Reader) Pos() int64 {
	off, err := r.r.Seek(0, io.SeekCurrent)
	if err != nil {
		return -1
	}
	return off
}

// Read forwards payload reads to the underlying stream.
func (r *Reader) Read(p []byte) (int, error) { return r.r.Read(p) }

// ReadAt reads payload bytes without moving the stream position.
func (r *Reader) ReadAt(p []byte, off int64) (int, error) { return r.r.ReadAt(p, off) }

// Seek moves the underlying stream. Callers are expected to stay inside the
// current container frame.
func (r *Reader) Seek(off int64, whence int) (int64, error) { return r.r.Seek(off, whence) }

// Next reads the next element header at the current position. It returns
// io.EOF when the current container is exhausted. stopIDs terminate an
// unknown-length parent: when the next ID is one of stopIDs, EOF is returned
// without consuming any bytes.
func (r *Reader) Next(stopIDs ...ElementID) (*Header, error) {
	off := r.Pos()
	if top, ok := r.top(); ok {
		if top.end != UnknownSize {
			if off > top.end {
				return nil, timeline.New(timeline.CatOverflow, "reader escaped container").WithOffset(off)
			}
			if off == top.end {
				return nil, io.EOF
			}
		} else {
			// Live (unknown-length) containers end at a registered stop ID
			// or at physical end of stream.
			id, err := r.peekID(off)
			if err == io.EOF {
				return nil, io.EOF
			}
			if err != nil {
				return nil, err
			}
			if top.stop[id] {
				return nil, io.EOF
			}
		}
	}
	h, err := r.readHeaderAt(off)
	if err != nil {
		return nil, err
	}
	return h, nil
}

// Enter pushes h as a container frame and enforces the nesting limit.
func (r *Reader) Enter(h *Header) error {
	if len(r.stack) >= MaxDepth {
		return timeline.New(timeline.CatCorrupt, "ebml nesting depth exceeded limit").
			WithOffset(h.Offset)
	}
	stop := make(map[ElementID]bool, len(r.stack)+1)
	if len(r.stack) > 0 {
		for id := range r.stack[len(r.stack)-1].stop {
			stop[id] = true
		}
	}
	r.stack = append(r.stack, frame{end: h.End(), stop: stop})
	if _, err := r.r.Seek(h.DataOffset, io.SeekStart); err != nil {
		return timeline.New(timeline.CatIO, "seek into element failed").WithOffset(h.DataOffset).Wrapf(err)
	}
	return nil
}

// Exit closes the current container, seeking to its end when the size is known.
func (r *Reader) Exit() error {
	n := len(r.stack)
	if n == 0 {
		return timeline.New(timeline.CatIO, "ebml stack underflow")
	}
	top := r.stack[n-1]
	r.stack = r.stack[:n-1]
	if top.end != UnknownSize {
		if _, err := r.r.Seek(top.end, io.SeekStart); err != nil {
			return timeline.New(timeline.CatIO, "seek to container end failed").WithOffset(top.end).Wrapf(err)
		}
	}
	return nil
}

// Skip moves the stream past a known-size element. Unknown-size elements
// cannot be skipped without walking their children.
func (r *Reader) Skip(h *Header) error {
	if h.Size == UnknownSize {
		return timeline.New(timeline.CatUnsupported, "cannot skip unknown-length element").
			WithOffset(h.Offset)
	}
	if _, err := r.r.Seek(h.End(), io.SeekStart); err != nil {
		return timeline.New(timeline.CatIO, "seek past element failed").WithOffset(h.End()).Wrapf(err)
	}
	return nil
}

func (r *Reader) top() (frame, bool) {
	if len(r.stack) == 0 {
		return frame{}, false
	}
	return r.stack[len(r.stack)-1], true
}

func (r *Reader) peekID(off int64) (ElementID, error) {
	var first [1]byte
	if n, err := r.r.ReadAt(first[:], off); err != nil {
		if err == io.EOF || (n == 0 && err != nil) {
			return 0, io.EOF
		}
		return 0, trunc(off, err)
	}
	n := vintLen(first[0])
	buf := make([]byte, n)
	buf[0] = first[0]
	if n > 1 {
		if _, err := io.ReadFull(io.NewSectionReader(r.r, off+1, int64(n-1)), buf[1:]); err != nil {
			return 0, trunc(off, err)
		}
	}
	id, _, err := decodeVINT(buf, true)
	return ElementID(id), err
}

func (r *Reader) readHeaderAt(off int64) (*Header, error) {
	var first [1]byte
	if _, err := io.ReadFull(r.r, first[:]); err != nil {
		// A clean end at stream boundary is EOF; mid-element truncation
		// inside a container remains a corrupt-input error.
		if err == io.EOF && len(r.stack) == 0 {
			return nil, io.EOF
		}
		return nil, trunc(off, err)
	}
	idLen := vintLen(first[0])
	idBuf := make([]byte, idLen)
	idBuf[0] = first[0]
	if _, err := io.ReadFull(r.r, idBuf[1:]); err != nil {
		return nil, trunc(off, err)
	}
	id, _, err := decodeVINT(idBuf, true)
	if err != nil {
		return nil, timeline.New(timeline.CatCorrupt, "invalid element id").WithOffset(off).Wrapf(err)
	}
	var sFirst [1]byte
	if _, err := io.ReadFull(r.r, sFirst[:]); err != nil {
		return nil, trunc(off, err)
	}
	sizeLen := vintLen(sFirst[0])
	sizeBuf := make([]byte, sizeLen)
	sizeBuf[0] = sFirst[0]
	if _, err := io.ReadFull(r.r, sizeBuf[1:]); err != nil {
		return nil, trunc(off, err)
	}
	raw, unknown, err := decodeVINT(sizeBuf, false)
	if err != nil {
		return nil, timeline.New(timeline.CatCorrupt, "invalid element size").WithOffset(off).Wrapf(err)
	}
	h := &Header{ID: ElementID(id), Offset: off, HeaderLen: idLen + sizeLen}
	h.DataOffset = off + int64(h.HeaderLen)
	if unknown {
		h.Size = UnknownSize
		return h, nil
	}
	if raw > uint64(MaxElementPayload) {
		return nil, timeline.New(timeline.CatOverflow, "element payload exceeds size limit").
			WithOffset(off)
	}
	h.Size = int64(raw)
	if h.End() < h.DataOffset {
		return nil, timeline.New(timeline.CatOverflow, "element end offset overflow").WithOffset(off)
	}
	return h, nil
}

// vintLen returns the VINT length indicated by the first byte's zero bits.
func vintLen(first byte) int {
	for i := 0; i < 8; i++ {
		if first&(0x80>>i) != 0 {
			return i + 1
		}
	}
	return 8
}

func decodeVINT(b []byte, isID bool) (value uint64, unknown bool, err error) {
	n := len(b)
	if n == 0 || n > 8 || vintLen(b[0]) != n {
		return 0, false, io.ErrUnexpectedEOF
	}
	if isID && n > 4 {
		return 0, false, timeline.New(timeline.CatUnsupported, "element id longer than 4 bytes")
	}
	// mask exposes the data bits of the first VINT byte for the all-ones test.
	mask := byte(0xFF >> uint(n)) // strip the length marker bits
	// Element IDs keep their VINT marker bits (the ID is the whole VINT);
	// size VINTs encode a numeric value with the marker stripped.
	v := uint64(b[0] & mask)
	if isID {
		v = uint64(b[0])
	}
	allOnes := (b[0] & mask) == mask
	for _, c := range b[1:] {
		v = v<<8 | uint64(c)
		if c != 0xFF {
			allOnes = false
		}
	}
	if !isID && allOnes {
		return v, true, nil
	}
	return v, false, nil
}

func trunc(off int64, err error) error {
	if err == io.EOF || err == io.ErrUnexpectedEOF {
		return timeline.New(timeline.CatCorrupt, "truncated ebml element").
			WithOffset(off).Wrapf(io.ErrUnexpectedEOF)
	}
	return timeline.New(timeline.CatIO, "stream read failed").WithOffset(off).Wrapf(err)
}
