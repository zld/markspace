package ebml

import (
	"bytes"
	"encoding/binary"
	"io"

	"mediacheck/timeline"
)

// EncodeID renders an EBML element identifier. The VINT marker is included in
// id as stored, so callers pass the canonical EBML/Matroska ID bytes.
func EncodeID(id uint32) []byte {
	out := make([]byte, 4)
	binary.BigEndian.PutUint32(out, id)
	for len(out) > 1 && out[0] == 0 {
		out = out[1:]
	}
	return out
}

// EncodeSize renders a data-size VINT. length 0 selects the shortest encoding;
// use EncodeUnknownSize for the reserved live-element pattern.
func EncodeSize(size uint64, length int) ([]byte, error) {
	if length == 0 {
		for length = 1; length <= 8; length++ {
			if size <= vintCapacity(length) {
				break
			}
		}
	}
	if length < 1 || length > 8 {
		return nil, timeline.New(timeline.CatConfig, "invalid vint length")
	}
	if size > vintCapacity(length) {
		return nil, timeline.New(timeline.CatOverflow, "size does not fit vint")
	}
	buf := make([]byte, length)
	marker := byte(1) << uint(8-length)
	v := size | uint64(marker)<<uint(8*(length-1))
	for i := 0; i < length; i++ {
		buf[i] = byte(v >> uint(8*(length-1-i)))
	}
	return buf, nil
}

// vintCapacity is the largest payload size encodable in an n-byte VINT.
func vintCapacity(n int) uint64 {
	if n == 8 {
		return 1<<63 - 1
	}
	return (uint64(1) << uint(7*n-1)) - 1
}

// EncodeUnknownSize returns the reserved all-ones size VINT of the given width.
func EncodeUnknownSize(length int) ([]byte, error) {
	if length < 1 || length > 8 {
		return nil, timeline.New(timeline.CatConfig, "invalid vint length")
	}
	buf := make([]byte, length)
	for i := range buf {
		buf[i] = 0xFF
	}
	// Re-apply the VINT length marker on the all-ones leading byte:
	// n-byte VINT first byte is (1 << (8-n)) | (all data bits set).
	buf[0] = byte(0xFF >> uint(length-1))
	return buf, nil
}

// ElementWriter emits one EBML element with a known payload size.
type ElementWriter struct{ w io.Writer }

// NewElementWriter wraps an output stream.
func NewElementWriter(w io.Writer) *ElementWriter { return &ElementWriter{w: w} }

// WriteElement emits id + size + payload.
func (e *ElementWriter) WriteElement(id ElementID, payload []byte) error {
	size, err := EncodeSize(uint64(len(payload)), 0)
	if err != nil {
		return err
	}
	if _, err := e.w.Write(EncodeID(uint32(id))); err != nil {
		return timeline.New(timeline.CatIO, "write element id failed").Wrapf(err)
	}
	if _, err := e.w.Write(size); err != nil {
		return timeline.New(timeline.CatIO, "write element size failed").Wrapf(err)
	}
	if _, err := e.w.Write(payload); err != nil {
		return timeline.New(timeline.CatIO, "write element payload failed").Wrapf(err)
	}
	return nil
}

// BuildElement returns the serialized bytes of one element.
func BuildElement(id ElementID, payload []byte) ([]byte, error) {
	var buf bytes.Buffer
	if err := NewElementWriter(&buf).WriteElement(id, payload); err != nil {
		return nil, err
	}
	return buf.Bytes(), nil
}

// ReadPayload reads a bounded element payload; reads past the size limit or
// unexpected EOF are reported as corrupt input at the element offset.
func (r *Reader) ReadPayload(h *Header) ([]byte, error) {
	if h.Size == UnknownSize {
		return nil, timeline.New(timeline.CatUnsupported, "unknown-length element has no fixed payload").
			WithOffset(h.Offset)
	}
	buf := make([]byte, h.Size)
	if _, err := io.ReadFull(r.r, buf); err != nil {
		return nil, timeline.New(timeline.CatCorrupt, "truncated element payload").
			WithOffset(h.Offset).Wrapf(io.ErrUnexpectedEOF)
	}
	return buf, nil
}
