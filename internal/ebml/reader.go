// Package ebml reads the Matroska/WebM element tree used by offline delivery
// checks. It is deliberately not a decoder: payloads are surfaced as raw byte
// spans with their absolute input offsets so callers can validate structure and
// re-read bytes without trusting any intermediate copy.
package ebml

import (
	"bufio"
	"errors"
	"io"

	"example.com/assetcheck/internal/timeline"
)

// Limits bound a hostile or malformed file. They are intentionally generous
// for real masters and tight against abusive nesting or huge single elements.
type Limits struct {
	// MaxElementSize caps a single known-length payload.
	MaxElementSize uint64
	// MaxDepth caps master nesting.
	MaxDepth int
	// MaxElements caps the total number of elements visited.
	MaxElements int
	// MaxUnknownSizeDepth caps how many unknown-length masters may be open at once.
	MaxUnknownSizeDepth int
}

// DefaultLimits are safe for delivery masters while still rejecting abusive
// inputs well below available memory.
func DefaultLimits() Limits {
	return Limits{
		MaxElementSize:      1 << 30, // 1 GiB per element
		MaxDepth:            24,
		MaxElements:         1 << 22, // ~4M elements
		MaxUnknownSizeDepth: 2,       // Segment + Cluster
	}
}

// Element is one parsed EBML element. Offset is the absolute byte offset of the
// element ID in the input. SizeKnown is false for unknown-length masters, which
// close only at a legal parent boundary.
type Element struct {
	ID         uint32
	Offset     int64
	HeaderSize int64
	Size       uint64
	SizeKnown  bool
	Unknown    bool
	Level      int
	End        int64 // absolute offset just past the payload
}

// Payload returns the absolute offset of the payload.
func (e Element) Payload() int64 { return e.Offset + e.HeaderSize }

// Name returns the human-readable Matroska name.
func (e Element) Name() string { return Name(e.ID) }

// Reader walks the element tree over a ReadSeeker using a bounded buffer. Call
// Next to obtain the next element at the current cursor; Descend to enter a
// master; call Next until io.EOF to finish. Payloads are never buffered whole:
// use ReadPayload for small leaf values or the reported offsets to stream.
//
// Unknown-length masters (Segment, Cluster) are closed when an element at the
// parent level appears; the boundary element is returned normally at the outer
// level. Because the reader is a single-lookahead design, callers must not
// re-scan payloads by hand — use ReadPayload so the cursor stays consistent.
type Reader struct {
	src    io.ReadSeeker
	buf    *bufio.Reader
	pos    int64
	size   int64
	limits Limits
	seen   int
	depth  int
	frames []frame
}

type frame struct {
	el       Element
	parentID uint32
}

// NewReader prepares a reader positioned at offset 0.
func NewReader(src io.ReadSeeker, limits Limits) (*Reader, error) {
	end, err := src.Seek(0, io.SeekEnd)
	if err != nil {
		return nil, timeline.Wrap(err, timeline.StageParse, timeline.CatIO, 0, -1,
			"measure input size")
	}
	if _, err := src.Seek(0, io.SeekStart); err != nil {
		return nil, timeline.Wrap(err, timeline.StageParse, timeline.CatIO, 0, -1,
			"seek to start")
	}
	return &Reader{
		src:    src,
		buf:    bufio.NewReaderSize(src, 64<<10),
		size:   end,
		limits: limits,
	}, nil
}

// Offset reports the current absolute input offset.
func (r *Reader) Offset() int64 { return r.pos }

// Size reports the total input size in bytes.
func (r *Reader) Size() int64 { return r.size }

// Depth reports the current master nesting depth (0 at top level).
func (r *Reader) Depth() int { return r.depth }

// ParentID reports the ID of the master containing the cursor, or 0 at top.
func (r *Reader) ParentID() uint32 {
	if len(r.frames) == 0 {
		return 0
	}
	return r.frames[len(r.frames)-1].el.ID
}

func (r *Reader) top() *frame {
	if len(r.frames) == 0 {
		return nil
	}
	return &r.frames[len(r.frames)-1]
}

func (r *Reader) readByte() (byte, error) {
	b, err := r.buf.ReadByte()
	if err != nil {
		return 0, err
	}
	r.pos++
	return b, nil
}

// readVint reads an EBML variable-length integer. For element IDs (id=true) the
// returned value keeps the length-marker bits exactly as stored on disk, so the
// numeric ID matches a hex dump. For sizes (id=false) the marker is stripped.
func (r *Reader) readVint(id bool) (val, marker uint64, rawLen int, err error) {
	start := r.pos
	first, err := r.readByte()
	if err != nil {
		return 0, 0, 0, err
	}
	if first == 0 {
		return 0, 0, 0, timeline.New(timeline.StageParse, timeline.CatIllegalLength, 0, start,
			"vint first byte is zero (invalid length marker)")
	}
	l := 1
	for bit := 7; bit >= 0; bit-- {
		if first&(1<<uint(bit)) != 0 {
			l = 8 - bit
			break
		}
	}
	marker = 1 << uint(8-l)
	val = uint64(first)
	if !id {
		val = uint64(first) & (marker - 1)
	}
	if id && l > 4 {
		return 0, 0, 0, timeline.New(timeline.StageParse, timeline.CatIllegalLength, 0, start,
			"element ID is %d bytes, max 4", l)
	}
	if l > 1 {
		rest := make([]byte, l-1)
		if _, err := io.ReadFull(r.buf, rest); err != nil {
			if errors.Is(err, io.EOF) || errors.Is(err, io.ErrUnexpectedEOF) {
				return 0, 0, 0, timeline.Wrap(io.ErrUnexpectedEOF, timeline.StageParse,
					timeline.CatTruncated, 0, start, "vint needs %d bytes, input ends at %d", l, r.size)
			}
			return 0, 0, 0, timeline.Wrap(err, timeline.StageParse, timeline.CatIO, 0, start,
				"read %d-byte vint tail", l)
		}
		r.pos += int64(len(rest))
		for _, b := range rest {
			val = (val << 8) | uint64(b)
		}
	}
	return val, marker, l, nil
}

// Next returns the next element at the current level. It closes every open
// unknown-length master that the element at the cursor is a sibling of. At a
// clean end of input it returns io.EOF; a master that promised more bytes than
// the input holds yields a structured truncated error.
func (r *Reader) Next() (Element, error) {
	if r.seen >= r.limits.MaxElements {
		return Element{}, timeline.New(timeline.StageParse, timeline.CatLimitExceeded, 0, r.pos,
			"element count exceeds limit %d", r.limits.MaxElements)
	}
	// Close known-length masters that ended exactly at the cursor.
	for len(r.frames) > 0 && !r.top().el.Unknown && r.pos >= r.top().el.End {
		r.frames = r.frames[:len(r.frames)-1]
		r.depth--
	}

	start := r.pos
	id, _, idLen, err := r.readVint(true)
	if err != nil {
		if errors.Is(err, io.EOF) {
			if f := r.top(); f != nil {
				if f.el.Unknown {
					// Unknown-length frames legally close at end of input.
					return Element{}, io.EOF
				}
				return Element{}, timeline.New(timeline.StageParse, timeline.CatTruncated, f.el.ID, start,
					"input ends at %d before declared end %d of %s", start, f.el.End, f.el.Name())
			}
			return Element{}, io.EOF
		}
		if errors.Is(err, io.ErrUnexpectedEOF) {
			var te *timeline.Error
			if errors.As(err, &te) {
				if f := r.top(); f != nil {
					te.ElementID = f.el.ID
				}
				return Element{}, te
			}
		}
		return Element{}, err
	}
	elemID := uint32(id)

	// Unknown-length boundary: close frames this ID is a sibling of.
	for f := r.top(); f != nil && f.el.Unknown && IsLevelSibling(f.el.ID, elemID); f = r.top() {
		r.frames = r.frames[:len(r.frames)-1]
		r.depth--
	}

	size, marker, sizeLen, err := r.readVint(false)
	if err != nil {
		// The ID has already been read, so the error belongs to that element
		// even though its size field is what ran off the end of input.
		var te *timeline.Error
		if errors.As(err, &te) {
			cp := *te
			cp.ElementID = elemID
			return Element{}, &cp
		}
		return Element{}, err
	}
	if sizeLen > 8 {
		return Element{}, timeline.New(timeline.StageParse, timeline.CatIllegalLength, elemID, start,
			"size field is %d bytes, max 8", sizeLen)
	}
	headerSize := int64(idLen + sizeLen)
	unknown := size == marker-1 && sizeLen > 0
	if !unknown && size > r.limits.MaxElementSize {
		return Element{}, timeline.New(timeline.StageParse, timeline.CatLimitExceeded, elemID, start,
			"element size %d exceeds limit %d", size, r.limits.MaxElementSize)
	}

	parent := r.ParentID()
	if unknown && !AllowsUnknownSize(elemID, parent) {
		return Element{}, timeline.New(timeline.StageParse, timeline.CatUnknownLength, elemID, start,
			"element %s may not declare unknown size inside %s", Name(elemID), Name(parent))
	}

	el := Element{
		ID:         elemID,
		Offset:     start,
		HeaderSize: headerSize,
		SizeKnown:  !unknown,
		Unknown:    unknown,
		Level:      Level(elemID),
	}
	if !unknown {
		el.Size = size
	}
	if !unknown {
		if size > 1<<62 || start+headerSize+int64(size) < start {
			return Element{}, timeline.New(timeline.StageParse, timeline.CatIllegalLength, elemID, start,
				"element size %d overflows the offset range", size)
		}
		el.End = start + headerSize + int64(size)
		if el.End > r.size {
			return Element{}, timeline.New(timeline.StageParse, timeline.CatTruncated, elemID, start,
				"declared size %d needs input through %d, input has %d bytes", size, el.End, r.size)
		}
		if f := r.top(); f != nil && !f.el.Unknown && el.End > f.el.End {
			return Element{}, timeline.New(timeline.StageParse, timeline.CatIllegalLength, elemID, start,
				"element %s ends at %d past parent %s end %d", el.Name(), el.End, f.el.Name(), f.el.End)
		}
	}
	r.seen++
	return el, nil
}

// Descend enters a master element.
func (r *Reader) Descend(el Element) error {
	if !IsMaster(el.ID) {
		return timeline.New(timeline.StageParse, timeline.CatIllegalLength, el.ID, el.Offset,
			"element %s is not a master", el.Name())
	}
	if r.depth >= r.limits.MaxDepth {
		return timeline.New(timeline.StageParse, timeline.CatLimitExceeded, el.ID, el.Offset,
			"master depth exceeds limit %d", r.limits.MaxDepth)
	}
	if el.Unknown {
		n := 0
		for _, f := range r.frames {
			if f.el.Unknown {
				n++
			}
		}
		if n >= r.limits.MaxUnknownSizeDepth {
			return timeline.New(timeline.StageParse, timeline.CatLimitExceeded, el.ID, el.Offset,
				"unknown-length nesting exceeds limit %d", r.limits.MaxUnknownSizeDepth)
		}
	}
	r.frames = append(r.frames, frame{el: el, parentID: r.ParentID()})
	r.depth++
	return nil
}

// Skip advances past an element's payload so the next Next call resumes after it.
func (r *Reader) Skip(el Element) error {
	if el.Unknown {
		return timeline.New(timeline.StageParse, timeline.CatUnknownLength, el.ID, el.Offset,
			"cannot skip an unknown-length master without descending")
	}
	if _, err := r.src.Seek(el.End, io.SeekStart); err != nil {
		return timeline.Wrap(err, timeline.StageParse, timeline.CatIO, el.ID, -1, "skip payload")
	}
	r.buf.Reset(r.src)
	r.pos = el.End
	return nil
}

// ReadPayload reads up to max bytes of a leaf payload, then restores the cursor
// to the element end so iteration can continue. It never reads an unbounded
// master.
func (r *Reader) ReadPayload(el Element, max int64) ([]byte, error) {
	if el.Unknown {
		return nil, timeline.New(timeline.StageParse, timeline.CatUnknownLength, el.ID, el.Offset,
			"master %s has no flat payload", el.Name())
	}
	if int64(el.Size) > max {
		return nil, timeline.New(timeline.StageParse, timeline.CatLimitExceeded, el.ID, el.Offset,
			"payload %d exceeds read limit %d", el.Size, max)
	}
	if _, err := r.src.Seek(el.Payload(), io.SeekStart); err != nil {
		return nil, timeline.Wrap(err, timeline.StageParse, timeline.CatIO, el.ID, -1, "seek to payload")
	}
	b := make([]byte, el.Size)
	if _, err := io.ReadFull(r.src, b); err != nil {
		return nil, timeline.Wrap(err, timeline.StageParse, timeline.CatTruncated, el.ID, el.Offset,
			"read payload %d", el.Size)
	}
	if _, err := r.src.Seek(el.End, io.SeekStart); err != nil {
		return nil, timeline.Wrap(err, timeline.StageParse, timeline.CatIO, el.ID, -1, "restore after payload")
	}
	r.buf.Reset(r.src)
	r.pos = el.End
	return b, nil
}
