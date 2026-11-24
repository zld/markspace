package output

import "encoding/binary"

func box(typ string, payload []byte) []byte {
	if len(typ) != 4 {
		panic("output: box type must be four characters: " + typ)
	}
	out := make([]byte, 8+len(payload))
	binary.BigEndian.PutUint32(out[0:4], uint32(8+len(payload)))
	copy(out[4:8], typ)
	copy(out[8:], payload)
	return out
}

func fullBox(typ string, version byte, flags uint32, payload []byte) []byte {
	full := make([]byte, 4+len(payload))
	full[0] = version
	full[1] = byte(flags >> 16)
	full[2] = byte(flags >> 8)
	full[3] = byte(flags)
	copy(full[4:], payload)
	return box(typ, full)
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

func appendBytes(parts ...[]byte) []byte {
	var out []byte
	for _, p := range parts {
		out = append(out, p...)
	}
	return out
}

func ftypBox() []byte {
	payload := appendBytes(
		[]byte("isom"),
		u32(512),
		[]byte("isom"), []byte("iso2"), []byte("mp41"),
	)
	return box("ftyp", payload)
}

func mvhdBox(durationMS uint32) []byte {
	p := make([]byte, 96)
	binary.BigEndian.PutUint32(p[12:16], 1000) // timescale
	binary.BigEndian.PutUint32(p[16:20], durationMS)
	binary.BigEndian.PutUint32(p[20:24], 0x00010000) // rate 1.0
	binary.BigEndian.PutUint16(p[24:26], 0x0100)     // volume
	identity := []byte{
		0x00, 0x01, 0x00, 0x00, 0, 0, 0, 0, 0, 0, 0, 0,
		0, 0, 0, 0, 0x00, 0x01, 0x00, 0x00, 0, 0, 0, 0,
		0, 0, 0, 0, 0, 0, 0, 0, 0x40, 0x00, 0x00, 0x00,
	}
	copy(p[36:72], identity)
	binary.BigEndian.PutUint32(p[92:96], 3) // next track ID
	return fullBox("mvhd", 0, 0, p)
}

func tkhdBox(trackID, durationMS uint32, audio bool, width, height uint32) []byte {
	p := make([]byte, 80)
	binary.BigEndian.PutUint32(p[8:12], trackID)
	binary.BigEndian.PutUint32(p[16:20], durationMS)
	if audio {
		binary.BigEndian.PutUint16(p[38:40], 0x0100) // volume
	}
	identity := []byte{
		0x00, 0x01, 0x00, 0x00, 0, 0, 0, 0, 0, 0, 0, 0,
		0, 0, 0, 0, 0x00, 0x01, 0x00, 0x00, 0, 0, 0, 0,
		0, 0, 0, 0, 0, 0, 0, 0, 0x40, 0x00, 0x00, 0x00,
	}
	copy(p[40:76], identity)
	binary.BigEndian.PutUint32(p[76:80], width<<16)
	// Height starts at payload offset? Wait tkhd matrix ends at 72, width 72:76, height 76:80.
	// Correct the above layout by writing both explicitly below.
	binary.BigEndian.PutUint32(p[72:76], width<<16)
	binary.BigEndian.PutUint32(p[76:80], height<<16)
	return fullBox("tkhd", 0, 3, p)
}

func mdhdBox(timescale, duration uint32) []byte {
	p := make([]byte, 20)
	binary.BigEndian.PutUint32(p[8:12], timescale)
	binary.BigEndian.PutUint32(p[12:16], duration)
	binary.BigEndian.PutUint16(p[16:18], 0x55c4) // und language
	return fullBox("mdhd", 0, 0, p)
}

func hdlrBox(handler, name string) []byte {
	p := appendBytes(u32(0), []byte(handler), make([]byte, 12), []byte(name), []byte{0})
	return fullBox("hdlr", 0, 0, p)
}

func videoMediaHeaderBox() []byte {
	return fullBox("vmhd", 0, 1, make([]byte, 8))
}

func audioMediaHeaderBox() []byte {
	return fullBox("smhd", 0, 0, make([]byte, 4))
}

func drefBox() []byte {
	url := fullBox("url ", 0, 1, nil)
	return fullBox("dref", 0, 0, appendBytes(u32(1), url))
}

func dinfBox() []byte {
	return box("dinf", drefBox())
}
