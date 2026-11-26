package cmaf

import "encoding/binary"

// BoxBuilder is a tiny deterministic ISO BMFF box writer used by the fixture
// generator and by tests. It deliberately covers only the box shapes this
// project emits (compact 32-bit headers, full boxes, nested containers);
// go-mp4 remains the authoritative reader for everything on disk.
type BoxBuilder struct {
	typ     string
	payload []byte
}

// NewBox starts a container/payload box of the given four-character type.
func NewBox(typ string) *BoxBuilder { return &BoxBuilder{typ: typ} }

// Bytes appends raw payload bytes.
func (b *BoxBuilder) Bytes(p []byte) *BoxBuilder {
	b.payload = append(b.payload, p...)
	return b
}

// FullHeader prepends a version/flags full-box header (version byte + 3 flag
// bytes) to the payload.
func (b *BoxBuilder) FullHeader(version byte, flags uint32) *BoxBuilder {
	hdr := []byte{version, byte(flags >> 16), byte(flags >> 8), byte(flags)}
	b.payload = append(hdr, b.payload...)
	return b
}

// U32/U64 helpers append big-endian integers.
func (b *BoxBuilder) U32(v uint32) *BoxBuilder {
	buf := make([]byte, 4)
	binary.BigEndian.PutUint32(buf, v)
	b.payload = append(b.payload, buf...)
	return b
}

func (b *BoxBuilder) U64(v uint64) *BoxBuilder {
	buf := make([]byte, 8)
	binary.BigEndian.PutUint64(buf, v)
	b.payload = append(b.payload, buf...)
	return b
}

// Child nests another builder's bytes as payload.
func (b *BoxBuilder) Child(c *BoxBuilder) *BoxBuilder {
	b.payload = append(b.payload, c.Build()...)
	return b
}

// Build serializes the box with its 8-byte header.
func (b *BoxBuilder) Build() []byte {
	out := make([]byte, 0, 8+len(b.payload))
	hdr := make([]byte, 8)
	binary.BigEndian.PutUint32(hdr[0:4], uint32(8+len(b.payload)))
	copy(hdr[4:8], []byte(fixType(b.typ)))
	out = append(out, hdr...)
	out = append(out, b.payload...)
	return out
}

func fixType(t string) string {
	if len(t) == 4 {
		return t
	}
	buf := make([]byte, 4)
	copy(buf, t)
	for i := len(t); i < 4; i++ {
		buf[i] = ' '
	}
	return string(buf)
}
