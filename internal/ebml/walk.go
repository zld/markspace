package ebml

import (
	"io"

	"github.com/local/tracecheck/internal/clock"
)

// IsMaster reports whether id belongs to the Matroska/WebM subset's master
// elements. Only master payloads hold nested EBML elements; descending into
// a leaf payload (e.g. SimpleBlock) would misparse media bytes as headers.
func IsMaster(id uint32) bool {
	switch id {
	case idEBMLHeader, idSegment, idInfo, idTracks, idTrackEntry,
		idCluster, idSeekHead, idSeek, idTags, idTag, idCues, idCuePoint:
		return true
	default:
		return false
	}
}

// Walk iterates element headers inside parent. A nil parent walks the whole
// document. The callback receives every element; descent into child
// elements happens automatically for master elements only.
// Unknown-length elements are legal only when they reach the enclosing
// boundary (parent end or EOF); encountering more sibling bytes after one
// means the element did not terminate at the boundary, which is reported as
// clock.CodeBoundary.
func (rd *Reader) Walk(parent *Element, fn func(el *Element, depth int) error) error {
	start, err := rd.r.Seek(0, io.SeekCurrent)
	if err != nil {
		return err
	}
	if parent != nil {
		start = parent.DataOffset
	}
	return NewReader(rd.r, rd.end).walk(parent, start, rd.end, 0, fn)
}

// walk reads siblings inside the region [start, boundary). The receiver's
// own end matches boundary, so ReadElementHeader bound checks are correct.
func (rd *Reader) walk(parent *Element, start, boundary int64, depth int, fn func(el *Element, depth int) error) error {
	if boundary < 0 {
		return clock.NewError(clock.CodeBoundary, start, parentPath(parent),
			"cannot walk region without a resolvable boundary")
	}
	if _, err := rd.r.Seek(start, io.SeekStart); err != nil {
		return err
	}

	for {
		pos, err := rd.r.Seek(0, io.SeekCurrent)
		if err != nil {
			return err
		}
		if pos >= boundary {
			return nil
		}
		el, err := rd.ReadElementHeader(parent)
		if err != nil {
			if err == io.EOF {
				return nil
			}
			if err == io.ErrUnexpectedEOF {
				return clock.NewError(clock.CodeTruncatedElement, pos, parentPath(parent),
					"element header truncated before boundary")
			}
			return err
		}
		if err := fn(el, depth); err != nil {
			return err
		}
		descent := IsMaster(el.ID)
		if el.Size < 0 {
			// Unknown length: payload runs to the enclosing boundary. At
			// the generic EBML level an unknown-length element consumes the
			// remaining parent bytes by definition; schema-level callers
			// (the Matroska layer) enforce whether that placement is legal.
			if descent {
				sub := NewReader(rd.r, boundary)
				if err := sub.walk(el, el.DataOffset, boundary, depth+1, fn); err != nil {
					return err
				}
			}
			if _, err := rd.r.Seek(boundary, io.SeekStart); err != nil {
				return err
			}
			continue
		}
		if descent {
			sub := NewReader(rd.r, el.EndOffset())
			if err := sub.walk(el, el.DataOffset, el.EndOffset(), depth+1, fn); err != nil {
				return err
			}
		}
		if _, err := rd.r.Seek(el.EndOffset(), io.SeekStart); err != nil {
			return err
		}
	}
}
