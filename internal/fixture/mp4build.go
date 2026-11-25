package fixture

import "encoding/binary"

// Box builds a big-endian ISOBMFF box. Fixtures are intentionally small and
// all sizes fit uint32.
func Box(typ string, payload ...[]byte) []byte {
	var body []byte
	for _, p := range payload {
		body = append(body, p...)
	}
	out := make([]byte, 8+len(body))
	binary.BigEndian.PutUint32(out[0:4], uint32(len(out)))
	copy(out[4:8], typ)
	copy(out[8:], body)
	return out
}

// FullBox builds a version/flags full box.
func FullBox(typ string, version byte, flags uint32, payload []byte) []byte {
	vf := []byte{version, byte((flags >> 16) & 0xff), byte((flags >> 8) & 0xff), byte(flags & 0xff)}
	return Box(typ, vf, payload)
}

func FtypBox() []byte {
	p := make([]byte, 12)
	copy(p[0:4], "isom")
	binary.BigEndian.PutUint32(p[4:8], 0x00000200)
	copy(p[8:12], "isom")
	return Box("ftyp", p)
}

type ElstEntrySpec struct {
	Duration  uint32
	MediaTime int32
	RateInt   int16
	RateFrac  int16
}

func ElstBox(entries ...ElstEntrySpec) []byte {
	p := make([]byte, 4+len(entries)*12)
	binary.BigEndian.PutUint32(p[0:4], uint32(len(entries)))
	for i, e := range entries {
		off := 4 + i*12
		binary.BigEndian.PutUint32(p[off:off+4], e.Duration)
		binary.BigEndian.PutUint32(p[off+4:off+8], uint32(e.MediaTime))
		binary.BigEndian.PutUint16(p[off+8:off+10], uint16(e.RateInt))
		binary.BigEndian.PutUint16(p[off+10:off+12], uint16(e.RateFrac))
	}
	return FullBox("elst", 0, 0, p)
}

func MdhdBox(timescale, duration uint32) []byte {
	p := make([]byte, 20)
	binary.BigEndian.PutUint32(p[8:12], timescale)
	binary.BigEndian.PutUint32(p[12:16], duration)
	// Language "und": undefined language.
	binary.BigEndian.PutUint16(p[16:18], 0x55c4)
	return FullBox("mdhd", 0, 0, p)
}

func HdlrBox() []byte {
	name := []byte("VideoHandler\x00")
	p := make([]byte, 24+len(name))
	binary.BigEndian.PutUint32(p[0:4], 0) // pre-defined
	copy(p[4:8], "vide")
	// p[8:20] is three reserved uint32 fields.
	copy(p[20:], name)
	return FullBox("hdlr", 0, 0, p)
}

func SttsBox(count uint32, delta uint32) []byte {
	p := make([]byte, 12)
	binary.BigEndian.PutUint32(p[0:4], 1)
	binary.BigEndian.PutUint32(p[4:8], count)
	binary.BigEndian.PutUint32(p[8:12], delta)
	return FullBox("stts", 0, 0, p)
}

func CttsBox(count uint32, offset int32) []byte {
	p := make([]byte, 12)
	binary.BigEndian.PutUint32(p[0:4], 1)
	binary.BigEndian.PutUint32(p[4:8], count)
	binary.BigEndian.PutUint32(p[8:12], uint32(offset))
	return FullBox("ctts", 1, 0, p)
}

func StssBox(samples ...uint32) []byte {
	p := make([]byte, 4+4*len(samples))
	binary.BigEndian.PutUint32(p[0:4], uint32(len(samples)))
	for i, n := range samples {
		binary.BigEndian.PutUint32(p[4+i*4:8+i*4], n)
	}
	return FullBox("stss", 0, 0, p)
}

func StszBox(sampleSize uint32, sizes ...uint32) []byte {
	p := make([]byte, 8+4*len(sizes))
	binary.BigEndian.PutUint32(p[0:4], sampleSize)
	binary.BigEndian.PutUint32(p[4:8], uint32(len(sizes)))
	for i, n := range sizes {
		binary.BigEndian.PutUint32(p[8+i*4:12+i*4], n)
	}
	return FullBox("stsz", 0, 0, p)
}

func StcoBox(offsets ...uint32) []byte {
	p := make([]byte, 4+4*len(offsets))
	binary.BigEndian.PutUint32(p[0:4], uint32(len(offsets)))
	for i, n := range offsets {
		binary.BigEndian.PutUint32(p[4+i*4:8+i*4], n)
	}
	return FullBox("stco", 0, 0, p)
}
