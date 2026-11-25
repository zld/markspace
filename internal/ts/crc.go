package ts

// mpeg2CRCTable is the CRC-32/MPEG-2 table: poly 0x04C11DB7, MSB-first,
// init 0xFFFFFFFF, no reflection, no final xor.
var mpeg2CRCTable = func() [256]uint32 {
	var t [256]uint32
	for i := 0; i < 256; i++ {
		c := uint32(i) << 24
		for j := 0; j < 8; j++ {
			if c&0x80000000 != 0 {
				c = (c << 1) ^ 0x04C11DB7
			} else {
				c <<= 1
			}
		}
		t[i] = c
	}
	return t
}()

// CRC32MPEG computes the CRC used by MPEG-TS PSI sections over b.
func CRC32MPEG(b []byte) uint32 {
	crc := uint32(0xFFFFFFFF)
	for _, x := range b {
		crc = (crc << 8) ^ mpeg2CRCTable[byte(crc>>24)^x]
	}
	return crc
}

// CheckSectionCRC verifies the trailing 4-byte big-endian CRC of a PSI
// section. The CRC field itself is excluded from the computation.
func CheckSectionCRC(section []byte) bool {
	if len(section) < 4 {
		return false
	}
	want := uint32(section[len(section)-4])<<24 |
		uint32(section[len(section)-3])<<16 |
		uint32(section[len(section)-2])<<8 |
		uint32(section[len(section)-1])
	return CRC32MPEG(section[:len(section)-4]) == want
}
