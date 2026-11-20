package fixture

// MPEG-2 PSI CRC-32 (polynomial 0x04C11DB7, initial 0xFFFFFFFF).
var crcTable = buildCRCTable()

func buildCRCTable() [256]uint32 {
	var t [256]uint32
	for i := 0; i < 256; i++ {
		crc := uint32(i) << 24
		for bit := 0; bit < 8; bit++ {
			if crc&0x80000000 != 0 {
				crc = crc<<1 ^ 0x04C11DB7
			} else {
				crc <<= 1
			}
		}
		t[i] = crc
	}
	return t
}

func crc32(bs []byte) uint32 {
	crc := uint32(0xffffffff)
	for _, b := range bs {
		crc = crc<<8 ^ crcTable[byte(crc>>24)^b]
	}
	return crc
}

func withCRC(b []byte) []byte {
	c := crc32(b)
	return append(b, byte(c>>24), byte(c>>16), byte(c>>8), byte(c))
}

func sectionHeader(tableID uint8, ext uint16, version uint8, current bool, number, last uint8, bodyLen int) []byte {
	length := 5 + bodyLen + 4 // bytes following section_length through CRC
	cur := byte(0)
	if current {
		cur = 1
	}
	return []byte{
		tableID,
		0xb0 | byte(length>>8),
		byte(length),
		byte(ext >> 8), byte(ext),
		byte(0xc0 | (version&0x1f)<<1 | cur),
		number,
		last,
	}
}

// patSection encodes one PAT section. Entries are program -> PMT PID pairs;
// a PID of 0x10 with program 0 models the reserved NIT entry.
func patSection(tsid uint16, version uint8, current bool, number, last uint8, programs [][2]uint16) []byte {
	body := make([]byte, 0, len(programs)*4)
	for _, p := range programs {
		body = append(body, byte(p[0]>>8), byte(p[0]), 0xe0|byte(p[1]>>8), byte(p[1]))
	}
	sec := sectionHeader(0x00, tsid, version, current, number, last, len(body))
	sec = append(sec, body...)
	return withCRC(sec)
}

// stream describes one PMT elementary stream entry. Desc, when non-nil, is
// written verbatim as the es_info descriptor payload.
type stream struct {
	Type uint8
	PID  uint16
	Desc []byte
}

// pmtSection encodes one PMT section.
func pmtSection(program uint16, version uint8, current bool, number, last uint8, pcrPID uint16, streams []stream) []byte {
	body := []byte{0xe0 | byte(pcrPID>>8), byte(pcrPID), 0xf0, 0x00}
	for _, s := range streams {
		body = append(body, s.Type, 0xe0|byte(s.PID>>8), byte(s.PID), 0xf0|byte(len(s.Desc)>>8), byte(len(s.Desc)))
		body = append(body, s.Desc...)
	}
	sec := sectionHeader(0x02, program, version, current, number, last, len(body))
	sec = append(sec, body...)
	return withCRC(sec)
}

// padDescriptor builds an unrecognised private descriptor of n bytes (n must
// be at least 2) used to push a PMT across several TS packets.
func padDescriptor(n int) []byte {
	if n < 2 {
		n = 2
	}
	d := make([]byte, n)
	d[0] = 0x9f // private data specifier style tag, ignored by parsers
	d[1] = byte(n - 2)
	for i := 2; i < n; i++ {
		d[i] = 0xa5
	}
	return d
}

// corruptCRC flips the last CRC byte so a section fails validation.
func corruptCRC(sec []byte) []byte {
	out := append([]byte(nil), sec...)
	out[len(out)-1] ^= 0xff
	return out
}
