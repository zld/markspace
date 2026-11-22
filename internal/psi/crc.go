// Package psi implements a self-contained PSI parser: section
// reassembly across TS packets, CRC validation, and versioned
// PAT/PMT table assembly.
package psi

// mpeg2Poly is the MPEG-2 CRC32 polynomial (no reflection, init
// 0xFFFFFFFF, no final xor).
const mpeg2Poly = 0x04C11DB7

// Checksum computes the MPEG-2 CRC32 of data. A valid section including
// its CRC field checksums to zero.
func Checksum(data []byte) uint32 {
	crc := uint32(0xFFFFFFFF)
	for _, b := range data {
		crc ^= uint32(b) << 24
		for i := 0; i < 8; i++ {
			if crc&0x80000000 != 0 {
				crc = (crc << 1) ^ mpeg2Poly
			} else {
				crc <<= 1
			}
		}
	}
	return crc
}
