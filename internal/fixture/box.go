package fixture

import (
	"bytes"
	"encoding/binary"
)

// BoxType converts a four-character code to its on-wire uint32.
func BoxType(code string) uint32 { return binary.BigEndian.Uint32([]byte(code)) }

// Box writes a complete size-prefixed box (8-byte header) around payload.
func Box(code string, payload []byte) []byte {
	out := make([]byte, 8+len(payload))
	binary.BigEndian.PutUint32(out[0:4], uint32(8+len(payload)))
	binary.BigEndian.PutUint32(out[4:8], BoxType(code))
	copy(out[8:], payload)
	return out
}

// FullBox writes a version/flags full box.
func FullBox(code string, version uint8, flags uint32, body []byte) []byte {
	hdr := make([]byte, 4)
	hdr[0] = version
	hdr[1] = byte(flags >> 16)
	hdr[2] = byte(flags >> 8)
	hdr[3] = byte(flags)
	return Box(code, append(hdr, body...))
}

// U32 appends a big-endian uint32.
func U32(v uint32) []byte {
	b := make([]byte, 4)
	binary.BigEndian.PutUint32(b, v)
	return b
}

// U64 appends a big-endian uint64.
func U64(v uint64) []byte {
	b := make([]byte, 8)
	binary.BigEndian.PutUint64(b, v)
	return b
}

// U16 appends a big-endian uint16.
func U16(v uint16) []byte {
	b := make([]byte, 2)
	binary.BigEndian.PutUint16(b, v)
	return b
}

// Builder accumulates boxes into an in-memory file.
type Builder struct{ buf bytes.Buffer }

// Write appends a raw box or box tree.
func (b *Builder) Write(p []byte) { b.buf.Write(p) }

// Bytes returns the assembled file.
func (b *Builder) Bytes() []byte { return b.buf.Bytes() }

// PatchU32 overwrites a uint32 at an absolute file offset. It exists so boxes
// written before the mdat offset was known (stco entries) can be fixed up.
func (b *Builder) PatchU32(off int, v uint32) {
	binary.BigEndian.PutUint32(b.buf.Bytes()[off:off+4], v)
}

// Size is the current file length.
func (b *Builder) Size() int { return b.buf.Len() }
