package wire

import "encoding/binary"

// Buffer is a deterministic big-endian builder with offset helpers.
type Buffer struct {
	b []byte
}

func (b *Buffer) Bytes() []byte { return b.b }
func (b *Buffer) Len() int      { return len(b.b) }

func (b *Buffer) Byte(v byte) int {
	off := len(b.b)
	b.b = append(b.b, v)
	return off
}

func (b *Buffer) Raw(v []byte) int {
	off := len(b.b)
	b.b = append(b.b, v...)
	return off
}

func (b *Buffer) U16(v uint16) int {
	var a [2]byte
	binary.BigEndian.PutUint16(a[:], v)
	return b.Raw(a[:])
}

func (b *Buffer) U32(v uint32) int {
	var a [4]byte
	binary.BigEndian.PutUint32(a[:], v)
	return b.Raw(a[:])
}

func (b *Buffer) U64(v uint64) int {
	var a [8]byte
	binary.BigEndian.PutUint64(a[:], v)
	return b.Raw(a[:])
}

func (b *Buffer) I64(v int64) int { return b.U64(uint64(v)) }

func (b *Buffer) PatchU16(offset int, v uint16) {
	binary.BigEndian.PutUint16(b.b[offset:offset+2], v)
}

func (b *Buffer) PatchU32(offset int, v uint32) {
	binary.BigEndian.PutUint32(b.b[offset:offset+4], v)
}

func (b *Buffer) PatchU64(offset int, v uint64) {
	binary.BigEndian.PutUint64(b.b[offset:offset+8], v)
}
