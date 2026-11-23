package fixture

import (
	"encoding/binary"
	"hash/fnv"
)

// builder is a minimal big-endian MP4 byte writer. Building boxes by hand
// keeps full control over structural defects (truncation, bad lengths) that
// a compliant writer would refuse to emit.
type builder struct {
	buf []byte
}

func (b *builder) bytes() []byte { return b.buf }

func (b *builder) u8(v byte)        { b.buf = append(b.buf, v) }
func (b *builder) u16(v uint16)     { b.buf = binary.BigEndian.AppendUint16(b.buf, v) }
func (b *builder) u24(v uint32)     { b.buf = append(b.buf, byte(v>>16), byte(v>>8), byte(v)) }
func (b *builder) u32(v uint32)     { b.buf = binary.BigEndian.AppendUint32(b.buf, v) }
func (b *builder) i32(v int32)      { b.buf = binary.BigEndian.AppendUint32(b.buf, uint32(v)) }
func (b *builder) u64(v uint64)     { b.buf = binary.BigEndian.AppendUint64(b.buf, v) }
func (b *builder) raw(p []byte)     { b.buf = append(b.buf, p...) }
func (b *builder) fourcc(s string)  { b.buf = append(b.buf, s[0], s[1], s[2], s[3]) }
func (b *builder) zeros(n int)      { b.buf = append(b.buf, make([]byte, n)...) }

// box opens a container box and returns a close function that patches the
// size field. Full boxes set version+flags first via fullBox.
func (b *builder) box(typ string) func() {
	start := len(b.buf)
	b.u32(0) // size placeholder
	b.fourcc(typ)
	return func() {
		binary.BigEndian.PutUint32(b.buf[start:], uint32(len(b.buf)-start))
	}
}

// fullBox writes the version/flags payload prefix of a FullBox.
func (b *builder) fullBox(version byte, flags uint32) {
	b.u8(version)
	b.u24(flags)
}

// payloadBox writes a leaf box whose payload is supplied directly.
func (b *builder) payloadBox(typ string, payload []byte) {
	b.u32(uint32(8 + len(payload)))
	b.fourcc(typ)
	b.raw(payload)
}

// payload derives deterministic sample bytes from seed, track id and index.
// Bytes never leave the process; they stand in for encoded media.
func payloadBytes(seed int64, trackID uint32, index, size int) []byte {
	out := make([]byte, size)
	h := fnv.New64a()
	var tmp [8]byte
	binary.BigEndian.PutUint64(tmp[:], uint64(seed))
	h.Write(tmp[:])
	binary.BigEndian.PutUint64(tmp[:], uint64(trackID))
	h.Write(tmp[:])
	binary.BigEndian.PutUint64(tmp[:], uint64(index))
	h.Write(tmp[:])
	stream := h.Sum64()
	for i := range out {
		out[i] = byte(stream >> ((i % 8) * 8))
		if i%8 == 7 {
			stream = stream*6364136223846793005 + 1442695040888963407
		}
	}
	return out
}
