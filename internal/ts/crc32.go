package ts

import "rtpregress/internal/clock"

// mpeg2CRC32 is the CRC-32/MPEG-2 table (poly 0x04C11DB7, no reflection,
// init 0xFFFFFFFF, no final xor) used to verify PSI sections.
var mpeg2CRC32 [256]uint32

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
		mpeg2CRC32[i] = crc
	}
}

// crc32mpeg2 computes the CRC over a full PSI section. A valid section's CRC
// (including its trailing 4 checksum bytes) always checksums to zero.
func crc32mpeg2(b []byte) uint32 {
	crc := uint32(0xFFFFFFFF)
	for _, v := range b {
		crc = (crc << 8) ^ mpeg2CRC32[byte(crc>>24)^v]
	}
	return crc
}

// checkCRC validates the trailing CRC32 of a PSI section.
func checkCRC(section []byte, want uint32, offset int64) error {
	if len(section) < 4 {
		return errPSI(offset, "section shorter than CRC field")
	}
	if crc32mpeg2(section) != 0 {
		return clock.New(clock.CodeTSPSI, "ts:psi", offset, 0, 0,
			"PSI CRC mismatch: section claims 0x%08x", want)
	}
	return nil
}

func errPSI(offset int64, format string, args ...any) error {
	return clock.New(clock.CodeTSPSI, "ts:psi", offset, 0, 0, format, args...)
}
