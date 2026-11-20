// Package fixture generates deterministic sample MP4 files for tests and
// demos: an edit-list/B-frame file, a fragmented file with 32-bit tfdt
// wrap, and deliberately corrupted variants.
package fixture

import "encoding/binary"

// bw is a big-endian byte buffer builder.
type bw struct{ buf []byte }

func (w *bw) u8(v uint8)   { w.buf = append(w.buf, v) }
func (w *bw) u16(v uint16) { w.buf = append(w.buf, byte(v>>8), byte(v)) }
func (w *bw) u32(v uint32) {
	var b [4]byte
	binary.BigEndian.PutUint32(b[:], v)
	w.buf = append(w.buf, b[:]...)
}
func (w *bw) i32(v int32) { w.u32(uint32(v)) }
func (w *bw) u64(v uint64) {
	var b [8]byte
	binary.BigEndian.PutUint64(b[:], v)
	w.buf = append(w.buf, b[:]...)
}
func (w *bw) bytes(b []byte) { w.buf = append(w.buf, b...) }
func (w *bw) str(s string)   { w.buf = append(w.buf, s...) }
func (w *bw) zeros(n int)    { w.buf = append(w.buf, make([]byte, n)...) }

// boxOf wraps payload in a box header with a 32-bit size.
func boxOf(typ string, payload []byte) []byte {
	var w bw
	w.u32(uint32(len(payload) + 8))
	w.str(typ)
	w.bytes(payload)
	return w.buf
}

// fullBoxOf wraps payload with a full-box header (version + 24-bit flags).
func fullBoxOf(typ string, version byte, flags uint32, payload []byte) []byte {
	var h bw
	h.u8(version)
	h.u24(flags)
	return boxOf(typ, append(h.buf, payload...))
}

func (w *bw) u24(v uint32) { w.buf = append(w.buf, byte(v>>16), byte(v>>8), byte(v)) }

// lcg is a deterministic pseudo-random byte source for sample filler.
type lcg uint32

func (l *lcg) next() byte {
	*l = *l*1103515245 + 12345
	return byte(*l >> 24)
}

func fill(seed uint32, n int) []byte {
	l := lcg(seed)
	out := make([]byte, n)
	for i := range out {
		out[i] = l.next()
	}
	return out
}
