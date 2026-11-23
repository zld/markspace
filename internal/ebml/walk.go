// Package ebml implements a bounded, streaming reader for the subset of EBML
// used by Matroska and WebM. This file parses one element header and payload
// at a time.
package ebml

import (
	"errors"
	"fmt"
	"io"

	"mediad/internal/merr"
)

// readElement parses one header and, for leaves, its bounded payload.
func (w *Walker) readElement() (Event, error) {
	start := w.off
	id, err := w.readID()
	if err != nil {
		return Event{}, err
	}
	rawSize, unknown, err := w.readSize()
	if err != nil {
		return Event{}, err
	}
	dataOffset := w.off
	depth := len(w.stack)

	// A child header must start inside its known-size parent. Without this
	// check a bad length would make the walker read a sibling's bytes as a
	// header before the boundary test could fire.
	if depth > 0 {
		parent := w.stack[depth-1]
		if !parent.unknown && start >= parent.end {
			return Event{}, merr.New("ebml.read", merr.CodeTruncatedElement, start,
				"child header starts past parent boundary %d", parent.end)
		}
		if !parent.unknown && !unknown && dataOffset+int64(rawSize) > parent.end {
			// Leaf overshoot is reported when the payload is read; for masters
			// it is a structural inconsistency, so flag it now.
			if masters[id] {
				return Event{}, merr.New("ebml.read", merr.CodeTruncatedElement, start,
					"element 0x%X ends past parent boundary %d", uint32(id), parent.end)
			}
		}
	}

	if !masters[id] {
		if unknown {
			return Event{}, merr.New("ebml.read", merr.CodeUnknownSizeNotAllowed, start,
				"element 0x%X may not omit its size", uint32(id))
		}
		if rawSize > uint64(w.max) {
			return Event{}, merr.New("ebml.read", merr.CodeElementTooLarge, start,
				"element 0x%X declares %d bytes, limit %d", uint32(id), rawSize, w.max)
		}
		data := make([]byte, rawSize)
		if err := w.readFull(data); err != nil {
			code := merr.CodeTruncatedElement
			for _, s := range w.stack {
				if s.id == IDCluster {
					code = merr.CodeTruncatedCluster
				}
			}
			return Event{}, merr.New("ebml.read", code, start,
				"payload of 0x%X truncated: want %d bytes, got %d", uint32(id), rawSize, int64(len(data))-int64(remaining(err)))
		}
		return Event{
			Kind:         Leaf,
			ID:           id,
			Size:         rawSize,
			HeaderOffset: start,
			DataOffset:   dataOffset,
			Data:         data,
			Depth:        depth,
		}, nil
	}

	if unknown {
		if !unknownOK[id] {
			return Event{}, merr.New("ebml.read", merr.CodeUnknownSizeNotAllowed, start,
				"master 0x%X may not omit its size", uint32(id))
		}
	} else {
		if rawSize > uint64(^uint64(0)>>1) {
			return Event{}, merr.New("ebml.read", merr.CodeInvalidVINT, start,
				"element 0x%X size overflows int64", uint32(id))
		}
	}
	if depth >= maxDepth {
		return Event{}, merr.New("ebml.read", merr.CodeUnknownSizeNotAllowed, start,
			"nesting depth %d exceeds limit", depth)
	}
	sc := scope{id: id, start: start, unknown: unknown}
	if !unknown {
		sc.end = dataOffset + int64(rawSize)
	}
	w.stack = append(w.stack, sc)
	return Event{
		Kind:         Start,
		ID:           id,
		Size:         rawSize,
		HeaderOffset: start,
		DataOffset:   dataOffset,
		Unknown:      unknown,
		Depth:        depth,
	}, nil
}

// Next returns the next event, or io.EOF at a clean end of input.
func (w *Walker) Next() (Event, error) {
	for {
		if len(w.stack) > 0 {
			top := &w.stack[len(w.stack)-1]
			if !top.unknown && w.off >= top.end {
				if w.off > top.end {
					return Event{}, merr.New("ebml.next", merr.CodeTruncatedElement, w.off,
						"element crossed its parent boundary (parent end %d)", top.end)
				}
				ev := Event{Kind: End, ID: top.id, HeaderOffset: top.start, Depth: len(w.stack) - 1}
				w.stack = w.stack[:len(w.stack)-1]
				return ev, nil
			}
			if top.unknown {
				id, present, err := w.peekID()
				if err != nil {
					return Event{}, err
				}
				if !present {
					ev := Event{Kind: End, ID: top.id, HeaderOffset: top.start,
						Depth: len(w.stack) - 1, Unknown: true}
					w.stack = w.stack[:len(w.stack)-1]
					return ev, nil
				}
				if !validChild(top.id, id) {
					ev := Event{Kind: End, ID: top.id, HeaderOffset: top.start,
						Depth: len(w.stack) - 1, Unknown: true}
					w.stack = w.stack[:len(w.stack)-1]
					return ev, nil
				}
			}
		}

		ev, err := w.readElement()
		if err != nil {
			if errors.Is(err, io.EOF) && len(w.stack) == 0 {
				w.done = true
				return Event{}, io.EOF
			}
			if errors.Is(err, io.EOF) {
				code := merr.CodeTruncatedElement
				for _, s := range w.stack {
					if s.id == IDCluster {
						code = merr.CodeTruncatedCluster
					}
				}
				return Event{}, merr.New("ebml.next", code, w.off,
					"input ended inside %s", w.pathString())
			}
			return Event{}, err
		}
		return ev, nil
	}
}

func (w *Walker) pathString() string {
	if len(w.stack) == 0 {
		return "document"
	}
	return fmt.Sprintf("0x%X", uint32(w.stack[len(w.stack)-1].id))
}
