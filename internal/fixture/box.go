package fixture

import "encoding/binary"

// wb is a tiny big-endian byte builder.
type wb struct{ b []byte }

func (w *wb) u16(v uint16) {
	var a [2]byte
	binary.BigEndian.PutUint16(a[:], v)
	w.b = append(w.b, a[:]...)
}
func (w *wb) u32(v uint32) {
	var a [4]byte
	binary.BigEndian.PutUint32(a[:], v)
	w.b = append(w.b, a[:]...)
}
func (w *wb) i32(v int32)  { w.u32(uint32(v)) }
func (w *wb) p(n int)      { w.b = append(w.b, make([]byte, n)...) }
func (w *wb) raw(p []byte) { w.b = append(w.b, p...) }
func (w *wb) fourcc(s string) {
	if len(s) != 4 {
		panic("fourcc must be 4 chars: " + s)
	}
	w.b = append(w.b, s[0], s[1], s[2], s[3])
}

// box wraps payload with a standard 8-byte big-endian box header.
func box(typ string, payload []byte) []byte {
	var h [8]byte
	binary.BigEndian.PutUint32(h[0:4], uint32(8+len(payload)))
	copy(h[4:8], typ)
	out := make([]byte, 0, 8+len(payload))
	out = append(out, h[:]...)
	return append(out, payload...)
}

// fullBox prepends version+flags to payload and wraps it as a box.
func fullBox(typ string, version byte, flags uint32, payload []byte) []byte {
	head := []byte{version, byte(flags >> 16), byte(flags >> 8), byte(flags)}
	return box(typ, append(head, payload...))
}

func concat(parts ...[]byte) []byte {
	var out []byte
	for _, p := range parts {
		out = append(out, p...)
	}
	return out
}

func identityMatrix(w *wb) {
	w.u32(0x00010000)
	w.u32(0)
	w.u32(0)
	w.u32(0)
	w.u32(0x00010000)
	w.u32(0)
	w.u32(0)
	w.u32(0)
	w.u32(0x40000000)
}

// deterministicPayload fills n bytes with a reproducible xorshift stream.
func deterministicPayload(n int, seed uint64) []byte {
	b := make([]byte, n)
	x := seed
	if x == 0 {
		x = 0x9e3779b97f4a7c15
	}
	for i := range b {
		x ^= x << 13
		x ^= x >> 7
		x ^= x << 17
		b[i] = byte(x >> 24)
	}
	return b
}

func buildFtyp() []byte {
	var p wb
	p.fourcc("isom")
	p.u32(512)
	p.fourcc("isom")
	p.fourcc("iso2")
	p.fourcc("avc1")
	p.fourcc("mp41")
	return box("ftyp", p.b)
}

func u32be(v uint32) []byte {
	var w wb
	w.u32(v)
	return w.b
}
