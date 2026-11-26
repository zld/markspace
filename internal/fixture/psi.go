package fixture

// psiSection builds a PAT-style PSI section (table header, syntax, program loop)
// and appends a computed MPEG-2 CRC32.
func psiSection(tableID uint8, idExt uint16, version uint8, loop []byte) []byte {
	inner := 5 + len(loop) + 4
	sec := make([]byte, 3+inner)
	sec[0] = tableID
	sec[1] = 0xB0 | byte(inner>>8)
	sec[2] = byte(inner)
	sec[3] = byte(idExt >> 8)
	sec[4] = byte(idExt)
	sec[5] = 0xC1 | (version&0x1F)<<1
	copy(sec[8:], loop)
	putCRC(sec)
	return sec
}

// mpeg2CRC is the CRC-32/MPEG-2 nibble-independent table (poly 0x04C11DB7).
var mpeg2CRC [256]uint32

func init() {
	for i := uint32(0); i < 256; i++ {
		crc := i << 24
		for b := 0; b < 8; b++ {
			if crc&0x80000000 != 0 {
				crc = (crc << 1) ^ 0x04C11DB7
			} else {
				crc <<= 1
			}
		}
		mpeg2CRC[i] = crc
	}
}

// crc32mpeg2 checksums a section excluding its trailing 4 CRC bytes.
func crc32mpeg2(b []byte) uint32 {
	crc := uint32(0xFFFFFFFF)
	for _, v := range b {
		crc = (crc << 8) ^ mpeg2CRC[byte(crc>>24)^v]
	}
	return crc
}

// putCRC fills the trailing four bytes of a section with its CRC32.
func putCRC(sec []byte) {
	crc := crc32mpeg2(sec[:len(sec)-4])
	sec[len(sec)-4] = byte(crc >> 24)
	sec[len(sec)-3] = byte(crc >> 16)
	sec[len(sec)-2] = byte(crc >> 8)
	sec[len(sec)-1] = byte(crc)
}
