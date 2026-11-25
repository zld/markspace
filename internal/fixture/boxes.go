package fixture

import "encoding/binary"

type builder struct {
	buf []byte
}

func (b *builder) u16(v uint16) {
	var x [2]byte
	binary.BigEndian.PutUint16(x[:], v)
	b.buf = append(b.buf, x[:]...)
}

func (b *builder) u32(v uint32) {
	var x [4]byte
	binary.BigEndian.PutUint32(x[:], v)
	b.buf = append(b.buf, x[:]...)
}

func (b *builder) u64(v uint64) {
	var x [8]byte
	binary.BigEndian.PutUint64(x[:], v)
	b.buf = append(b.buf, x[:]...)
}

func (b *builder) bytes(p []byte) { b.buf = append(b.buf, p...) }

func box(typ string, payload []byte) []byte {
	out := make([]byte, 8+len(payload))
	binary.BigEndian.PutUint32(out[0:4], uint32(8+len(payload)))
	copy(out[4:8], typ)
	copy(out[8:], payload)
	return out
}

func fullBox(version byte, flags uint32, payload []byte) []byte {
	head := uint32(version)<<24 | flags&0xffffff
	b := builder{}
	b.u32(head)
	b.bytes(payload)
	return b.buf
}

func ftypBox() []byte {
	b := builder{}
	b.bytes([]byte("isom"))
	b.u32(0x200)
	b.bytes([]byte("isomiso2mp41"))
	return box("ftyp", b.buf)
}

func mvhdBox(timescale uint32, duration uint32) []byte {
	b := builder{}
	b.u32(0) // creation
	b.u32(0) // modification
	b.u32(timescale)
	b.u32(duration)
	b.u32(0x00010000) // rate
	b.u16(0x0100)     // volume
	b.u16(0)          // reserved
	b.u32(0)
	b.u32(0)
	for i := 0; i < 9; i++ {
		v := uint32(0)
		if i == 0 || i == 4 || i == 8 {
			v = 0x00010000
		}
		b.u32(v)
	}
	for i := 0; i < 6; i++ {
		b.u32(0)
	}
	b.u32(3) // next track ID
	return box("mvhd", fullBox(0, 0, b.buf))
}

func tkhdBox(id, duration uint32) []byte {
	b := builder{}
	b.u32(0)
	b.u32(0)
	b.u32(id)
	b.u32(0)
	b.u32(duration)
	b.u32(0)
	b.u32(0)
	b.u16(0) // layer
	b.u16(0) // alternate group
	b.u16(0) // volume
	b.u16(0) // reserved
	for i := 0; i < 9; i++ {
		v := uint32(0)
		if i == 0 || i == 4 || i == 8 {
			v = 0x00010000
		}
		b.u32(v)
	}
	b.u32(320 << 16) // width
	b.u32(180 << 16) // height
	return box("tkhd", fullBox(0, 7, b.buf))
}

func mdhdBox(timescale, duration uint32) []byte {
	b := builder{}
	b.u32(0)
	b.u32(0)
	b.u32(timescale)
	b.u32(duration)
	b.u16(0x55c4) // language "und"
	b.u16(0)
	return box("mdhd", fullBox(0, 0, b.buf))
}

func hdlrBox() []byte {
	b := builder{}
	b.u32(0)
	b.bytes([]byte("vide"))
	b.u32(0)
	b.u32(0)
	b.u32(0)
	b.bytes([]byte("VideoHandler"))
	b.buf = append(b.buf, 0)
	return box("hdlr", fullBox(0, 0, b.buf))
}

func stsdBox() []byte {
	b := builder{}
	b.u32(0) // entry count; metadata-only fixture
	return box("stsd", fullBox(0, 0, b.buf))
}

func sttsUniformBox(count, delta uint32) []byte {
	b := builder{}
	b.u32(1)
	b.u32(count)
	b.u32(delta)
	return box("stts", fullBox(0, 0, b.buf))
}

func cttsUniformBox(count, offset uint32) []byte {
	b := builder{}
	b.u32(1)
	b.u32(count)
	b.u32(offset)
	return box("ctts", fullBox(0, 0, b.buf))
}

func stssBox(numbers []uint32) []byte {
	b := builder{}
	b.u32(uint32(len(numbers)))
	for _, n := range numbers {
		b.u32(n)
	}
	return box("stss", fullBox(0, 0, b.buf))
}

func stscOneChunkBox(samples uint32) []byte {
	b := builder{}
	b.u32(1)
	b.u32(1)
	b.u32(samples)
	b.u32(1)
	return box("stsc", fullBox(0, 0, b.buf))
}

func stszUniformBox(count, size uint32) []byte {
	b := builder{}
	b.u32(size)
	b.u32(count)
	return box("stsz", fullBox(0, 0, b.buf))
}

func stcoOneBox(offset uint32) []byte {
	b := builder{}
	b.u32(1)
	b.u32(offset)
	return box("stco", fullBox(0, 0, b.buf))
}
