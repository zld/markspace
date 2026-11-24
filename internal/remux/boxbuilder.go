package remux

import (
	"bytes"
	"encoding/binary"
)

// box assembles a box with a back-filled 32-bit size.  Using one helper keeps
// every nested box's size exact by construction instead of by arithmetic.
type boxBuilder struct {
	buf bytes.Buffer
}

func (b *boxBuilder) start(typ string) int {
	if b.buf.Len() > 0 {
		// nested builders are used instead; this is a flat builder
	}
	off := b.buf.Len()
	hdr := make([]byte, 8)
	copy(hdr[4:], typ)
	b.buf.Write(hdr)
	return off
}

func (b *boxBuilder) finish(off int) {
	binary.BigEndian.PutUint32(b.buf.Bytes()[off:], uint32(b.buf.Len()-off))
}

func (b *boxBuilder) u8(v uint8) { b.buf.WriteByte(v) }
func (b *boxBuilder) u16(v uint16) {
	var t [2]byte
	binary.BigEndian.PutUint16(t[:], v)
	b.buf.Write(t[:])
}
func (b *boxBuilder) u24(v uint32) {
	b.buf.Write([]byte{byte(v >> 16), byte(v >> 8), byte(v)})
}
func (b *boxBuilder) u32(v uint32) {
	var t [4]byte
	binary.BigEndian.PutUint32(t[:], v)
	b.buf.Write(t[:])
}
func (b *boxBuilder) u64(v uint64) {
	var t [8]byte
	binary.BigEndian.PutUint64(t[:], v)
	b.buf.Write(t[:])
}
func (b *boxBuilder) zeros(n int)  { b.buf.Write(make([]byte, n)) }
func (b *boxBuilder) raw(p []byte) { b.buf.Write(p) }
func (b *boxBuilder) str(s string) { b.buf.WriteString(s) }

func (b *boxBuilder) full(version uint8, flags uint32) {
	b.u8(version)
	b.u24(flags)
}

func (b *boxBuilder) bytes() []byte { return b.buf.Bytes() }

// buildFtyp writes a minimal ftyp that declares the same major brand as the
// input and lists the brands this writer can guarantee.
func buildFtyp(brand string) []byte {
	if len(brand) != 4 {
		brand = "isom"
	}
	var b boxBuilder
	off := b.start("ftyp")
	b.str(brand)
	b.u32(0)
	b.str("isom")
	b.str("iso5")
	b.str("iso6")
	b.str("dash")
	b.finish(off)
	return b.bytes()
}
