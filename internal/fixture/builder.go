// Package fixture generates small, deterministic CMAF/fMP4 inputs and faults.
package fixture

import (
	"encoding/binary"
)

func box(typ string, payload ...[]byte) []byte {
	total := 8
	for _, p := range payload {
		total += len(p)
	}
	b := make([]byte, total)
	binary.BigEndian.PutUint32(b[0:4], uint32(total))
	copy(b[4:8], typ)
	pos := 8
	for _, p := range payload {
		copy(b[pos:], p)
		pos += len(p)
	}
	return b
}

func fullBox(typ string, version byte, flags uint32, payload ...[]byte) []byte {
	head := []byte{version, byte(flags >> 16), byte(flags >> 8), byte(flags)}
	parts := append([][]byte{head}, payload...)
	return box(typ, parts...)
}

func u16(v uint16) []byte {
	b := make([]byte, 2)
	binary.BigEndian.PutUint16(b, v)
	return b
}

func u32(v uint32) []byte {
	b := make([]byte, 4)
	binary.BigEndian.PutUint32(b, v)
	return b
}

func u64(v uint64) []byte {
	b := make([]byte, 8)
	binary.BigEndian.PutUint64(b, v)
	return b
}

func concat(parts ...[]byte) []byte {
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

func ftypBox() []byte {
	return box("ftyp",
		[]byte("iso5"),
		u32(0x200),
		[]byte("iso5iso6mp41dash"))
}

func mvhdBox() []byte {
	p := concat(
		u32(0), u32(0), u32(1000), u32(6_000_000),
		u32(0x00010000), u16(0x0100), make([]byte, 10),
		make([]byte, 36*2), u32(2),
	)
	return fullBox("mvhd", 0, 0, p)
}

func tkhdBox(id uint32) []byte {
	p := concat(
		u32(0), u32(0), u32(id), u32(0),
		u32(6_000_000), make([]byte, 8),
		u16(0), u16(0), u16(0x0010), u16(0),
		// identity matrix
		u32(0x00010000), u32(0), u32(0),
		u32(0), u32(0x00010000), u32(0),
		u32(0), u32(0), u32(0x40000000),
		u32(320<<16), u32(240<<16),
	)
	return fullBox("tkhd", 0, 7, p) // enabled/in-preview/in-movie flags
}

func mdhdBox(timescale uint32) []byte {
	p := concat(u32(0), u32(0), u32(timescale), u32(6_000_000), u16(0x55c4), u16(0))
	return fullBox("mdhd", 0, 0, p)
}

func hdlrBox(handler [4]byte, name string) []byte {
	p := concat(u32(0), handler[:], make([]byte, 12), []byte(name), []byte{0})
	return fullBox("hdlr", 0, 0, p)
}

func trexBox(trackID uint32) []byte {
	p := concat(
		u32(trackID), u32(1),
		u32(0), u32(0), u32(0),
	)
	return fullBox("trex", 0, 0, p)
}

func videoTrak() []byte {
	mdia := box("mdia",
		mdhdBox(30_000),
		hdlrBox([4]byte{'v', 'i', 'd', 'e'}, "VideoHandler"),
		box("minf", box("vmhd", fullBox("vmhd", 0, 1, u16(0), u16(0), u16(0))[8:])))
	return box("trak", tkhdBox(1), mdia)
}

func audioTrak() []byte {
	mdia := box("mdia",
		mdhdBox(48_000),
		hdlrBox([4]byte{'s', 'o', 'u', 'n'}, "SoundHandler"),
		box("minf", fullBox("smhd", 0, 0, u16(0), u16(0))))
	return box("trak", tkhdBox(2), mdia)
}

// InitSegment builds a minimal init segment with video track 1 and audio 2.
func InitSegment() []byte {
	mvex := box("mvex", trexBox(1), trexBox(2))
	moov := box("moov", mvhdBox(), videoTrak(), audioTrak(), mvex)
	return concat(ftypBox(), moov)
}
