package ebml

import (
	"io"

	"medtrace/internal/mederr"
)

// ReadBody materializes the full body of a known-length element.
func (r *Reader) ReadBody(e *Element) ([]byte, error) {
	if e.UnknownSize {
		return nil, mederr.At(mederr.CatUnknownLength, e.HeaderOffset,
			"element %s has unknown length; walk children instead", e.ID.Name())
	}
	if err := r.SeekTo(e.BodyOffset); err != nil {
		return nil, mederr.At(mederr.CatIO, e.BodyOffset, "seek failed").Wrap(err)
	}
	buf := make([]byte, e.Size)
	if _, err := io.ReadFull(r.r, buf); err != nil {
		return nil, mederr.At(mederr.CatTruncatedHeader, e.BodyOffset,
			"element %s body truncated", e.ID.Name()).Wrap(err)
	}
	return buf, nil
}

// WalkChildren visits every direct child of parent (pass nil for the stream
// root). Unknown-length elements end exactly at the parent boundary or EOF.
// The visitor is positioned at the element body; it must Seek away or call
// WalkChildren before control returns so the next header can be located.
func (r *Reader) WalkChildren(parent *Element, visit func(*Element) error) error {
	var boundary int64 = -1
	var path []string
	if parent != nil {
		if !parent.UnknownSize {
			boundary = parent.BodyOffset + parent.Size
		}
		path = append(append([]string(nil), parent.Path...), parent.ID.Name())
	}
	for {
		pos := r.Offset()
		if boundary >= 0 && pos >= boundary {
			return nil
		}
		e, err := r.ReadElementHeader(boundary)
		if err == io.EOF {
			return nil // unknown-length parent terminates at EOF
		}
		if err != nil {
			if se := mederr.AsError(err); se != nil && len(se.Path) == 0 {
				return se.WithPath(path)
			}
			return err
		}
		e.Path = append(append([]string(nil), path...), e.ID.Name())
		if err := visit(e); err != nil {
			if se := mederr.AsError(err); se != nil && len(se.Path) == 0 {
				return se.WithPath(e.Path)
			}
			return err
		}
		if e.UnknownSize {
			return nil // remainder belongs to this element
		}
		if err := r.SeekTo(e.End()); err != nil {
			return mederr.At(mederr.CatIO, e.End(), "seek to element end failed").Wrap(err)
		}
	}
}
