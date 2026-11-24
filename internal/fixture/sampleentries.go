package fixture

// Sample set sizes.  Small payloads keep the fixtures readable while still
// exercising multi-track offsets.
var (
	audioPayload   = []byte{0x21, 0x10, 0x05, 0x20, 0x74, 0x65, 0x73, 0x74}
	subPayload     = []byte{0x00, 0x05, 'h', 'e', 'l', 'l', 'o'}
	chapterPayload = []byte{0x00, 0x07, 'c', 'h', 'a', 'p', 't', 'e', 'r'}
	id3Payload     = buildID3("TIT2", []byte("fixture"))
)

func buildID3(frameID string, text []byte) []byte {
	var b wbuf
	b.str("ID3")
	b.u8(4)
	b.u8(0)
	b.u8(0)
	size := 10 + len(text)
	b.u8(byte(size >> 21 & 0x7f))
	b.u8(byte(size >> 14 & 0x7f))
	b.u8(byte(size >> 7 & 0x7f))
	b.u8(byte(size & 0x7f))
	b.str(frameID)
	b.u32(uint32(len(text)))
	b.u16(0)
	b.u8(0)
	b.raw(text)
	return b.bytes()
}

func avcC() []byte {
	var b wbuf
	b.u8(1)
	b.u8(0x42)
	b.u8(0x00)
	b.u8(0x1f)
	b.u8(0xff) // lengthSizeMinusOne = 3
	b.u8(0xe1)
	b.u16(2)
	b.raw([]byte{0x67, 0x42})
	b.u8(1)
	b.u16(2)
	b.raw([]byte{0x68, 0xce})
	return box("avcC", b.bytes())
}

func visualSampleEntry(typ string, extra ...[]byte) []byte {
	var b wbuf
	b.zeros(6)
	b.u16(1)
	b.u16(0)
	b.u16(0)
	b.zeros(12)
	b.u16(1280)
	b.u16(720)
	b.u32(0x00480000)
	b.u32(0x00480000)
	b.u32(0)
	b.u16(1)
	b.zeros(32)
	b.u16(0x0018)
	b.u16(0xffff)
	for _, e := range extra {
		b.raw(e)
	}
	return box(typ, b.bytes())
}

func audioSampleEntry() []byte {
	var b wbuf
	b.zeros(6)
	b.u16(1)
	b.zeros(8)
	b.u16(2)
	b.u16(16)
	b.u16(0)
	b.u16(0)
	b.u32(48000 << 16)
	return box("mp4a", b.bytes())
}

func subtitleSampleEntry() []byte {
	var b wbuf
	b.zeros(6)
	b.u16(1)
	b.str("http://www.w3.org/ns/ttml")
	b.u8(0)
	b.str("")
	b.u8(0)
	b.str("")
	b.u8(0)
	return box("stpp", b.bytes())
}

func metadataSampleEntry(typ string) []byte {
	var b wbuf
	b.zeros(6)
	b.u16(1)
	return box(typ, b.bytes())
}

func stsd(entries ...[]byte) []byte {
	var b wbuf
	b.u32(uint32(len(entries)))
	for _, e := range entries {
		b.raw(e)
	}
	return fullbox("stsd", 0, 0, b.bytes())
}

// seiPayload builds one user_data_unregistered SEI NAL plus filler NALs so the
// video sample exercises the length-prefixed SEI path rather than a single NAL.
func seiPayload(uuid string) []byte {
	msg := append([]byte(uuid), []byte("meta")...)
	var rbsp []byte
	rbsp = append(rbsp, 0x05)
	rbsp = append(rbsp, byte(len(msg)))
	rbsp = append(rbsp, msg...)
	sei := append([]byte{0x06}, rbsp...)
	var out wbuf
	out.u32(uint32(len(sei)))
	out.raw(sei)
	filler := []byte{0x09, 0xf0}
	out.u32(uint32(len(filler)))
	out.raw(filler)
	return out.bytes()
}
