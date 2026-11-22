// Package mp4box builds minimal, deterministic ISOBMFF byte streams for test
// fixtures. It deliberately supports only the boxes the kernel parses, which
// keeps generated corrupt inputs easy to reason about.
package mp4box

import (
	"encoding/binary"
	"io"
)

// Box is one ISOBMFF container or leaf box.
type Box struct {
	Type     string
	Payload  []byte // leaf payload (excludes header); nil for containers
	Children []*Box
}

// NewLeaf builds a leaf box.
func NewLeaf(typ string, payload []byte) *Box {
	return &Box{Type: typ, Payload: payload}
}

// NewContainer builds a box containing children.
func NewContainer(typ string, children ...*Box) *Box {
	return &Box{Type: typ, Children: children}
}

// FullHeader prepends a version/flags full-box header to payload fields.
func FullHeader(version uint8, flags uint32, fields []byte) []byte {
	out := make([]byte, 4+len(fields))
	out[0] = version
	out[1] = byte(flags >> 16)
	out[2] = byte(flags >> 8)
	out[3] = byte(flags)
	copy(out[4:], fields)
	return out
}

// U16/U32/U64 encode big-endian integers.
func U16(v uint16) []byte { b := make([]byte, 2); binary.BigEndian.PutUint16(b, v); return b }
func U32(v uint32) []byte { b := make([]byte, 4); binary.BigEndian.PutUint32(b, v); return b }
func U64(v uint64) []byte { b := make([]byte, 8); binary.BigEndian.PutUint64(b, v); return b }

// I32/I64 encode signed big-endian integers.
func I32(v int32) []byte { return U32(uint32(v)) }
func I64(v int64) []byte { return U64(uint64(v)) }

// Concat joins byte slices without a separator.
func Concat(parts ...[]byte) []byte {
	n := 0
	for _, p := range parts {
		n += len(p)
	}
	out := make([]byte, 0, n)
	for _, p := range parts {
		out = append(out, p...)
	}
	return out
}

// Size returns the wire size including the 8-byte header.
func (b *Box) Size() uint64 {
	if b.Children != nil {
		n := uint64(8)
		for _, c := range b.Children {
			n += c.Size()
		}
		return n
	}
	return uint64(8 + len(b.Payload))
}

// WriteTo serialises the box and satisfies io.WriterTo.
func (b *Box) WriteTo(w io.Writer) (int64, error) {
	hdr := make([]byte, 8)
	binary.BigEndian.PutUint32(hdr[0:4], uint32(b.Size()))
	copy(hdr[4:8], []byte(b.Type))
	n, err := w.Write(hdr)
	total := int64(n)
	if err != nil {
		return total, err
	}
	if b.Children != nil {
		for _, c := range b.Children {
			k, err := c.WriteTo(w)
			total += k
			if err != nil {
				return total, err
			}
		}
		return total, nil
	}
	k, err := w.Write(b.Payload)
	return total + int64(k), err
}

// Bytes serialises the whole tree into a freshly allocated byte slice.
func (b *Box) Bytes() []byte {
	var w sliceWriter
	if _, err := b.WriteTo(&w); err != nil {
		panic(err)
	}
	return []byte(w)
}

type sliceWriter []byte

func (s *sliceWriter) Write(p []byte) (int, error) {
	*s = append(*s, p...)
	return len(p), nil
}
