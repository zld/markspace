// Package ts: the CRC-32/MPEG-2 checksum used by PSI sections.
package ts

// MPEGCRC32 computes the CRC-32/MPEG-2 checksum used by PSI sections:
// polynomial 0x04C11DB7, initial value all ones, no input/output reflection
// and no final XOR.  A section with a correct trailing CRC yields zero.
func MPEGCRC32(data []byte) uint32 {
	crc := uint32(0xFFFFFFFF)
	for _, b := range data {
		crc ^= uint32(b) << 24
		for i := 0; i < 8; i++ {
			if crc&0x80000000 != 0 {
				crc = crc<<1 ^ 0x04C11DB7
			} else {
				crc <<= 1
			}
		}
	}
	return crc
}
