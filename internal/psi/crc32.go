package psi

// CRC-32/MPEG-2 as specified for MPEG-2 PSI sections: polynomial
// 0x04C11DB7, initial value 0xFFFFFFFF, no input/output reflection and no
// final XOR. Computing it over a complete section (including its trailing
// four CRC bytes) yields zero.
const crcPoly = 0x04C11DB7

var crcTable [256]uint32

func init() {
	for i := 0; i < 256; i++ {
		crc := uint32(i) << 24
		for bit := 0; bit < 8; bit++ {
			if crc&0x80000000 != 0 {
				crc = crc<<1 ^ crcPoly
			} else {
				crc <<= 1
			}
		}
		crcTable[i] = crc
	}
}

// CRC32 computes the MPEG-2 PSI CRC over bs.
func CRC32(bs []byte) uint32 {
	crc := uint32(0xffffffff)
	for _, b := range bs {
		crc = crc<<8 ^ crcTable[byte(crc>>24)^b]
	}
	return crc
}
