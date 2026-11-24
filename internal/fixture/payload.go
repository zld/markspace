// Package fixture generates small, deterministic local media and fault files.
// No binary media is checked into the repository.
package fixture

import (
	"encoding/binary"
	"hash/crc32"
)

const payloadMagic = "MDPK"

// Payload builds a synthetic compressed sample envelope. The envelope gives
// fault tests an explicit integrity signal without claiming the bytes decode
// as AVC/AAC; remux still preserves every byte unchanged.
func Payload(trackID uint32, index, size int) []byte {
	if size < headerSize {
		size = headerSize
	}
	b := make([]byte, size)
	copy(b[0:4], payloadMagic)
	binary.BigEndian.PutUint16(b[4:6], uint16(trackID))
	binary.BigEndian.PutUint16(b[6:8], uint16(index))
	binary.BigEndian.PutUint32(b[8:12], uint32(index*2654435761))
	for i := headerSize; i < size-4; i++ {
		b[i] = byte(trackID*31 + uint32(index)*7 + uint32(i)*13)
	}
	sum := crc32.ChecksumIEEE(b[:size-4])
	binary.BigEndian.PutUint32(b[size-4:], sum)
	return b
}

const headerSize = 12 // magic + track + index + pattern seed; CRC adds four bytes

// ValidatePayload checks the fixture envelope and CRC.
func ValidatePayload(b []byte, trackID uint32, index int) bool {
	if len(b) < headerSize+4 || string(b[:4]) != payloadMagic {
		return false
	}
	if binary.BigEndian.Uint16(b[4:6]) != uint16(trackID) {
		return false
	}
	if binary.BigEndian.Uint16(b[6:8]) != uint16(index) {
		return false
	}
	want := binary.BigEndian.Uint32(b[len(b)-4:])
	return crc32.ChecksumIEEE(b[:len(b)-4]) == want
}

// CorruptPayload flips one payload byte while leaving container metadata valid.
func CorruptPayload(b []byte) []byte {
	out := append([]byte(nil), b...)
	if len(out) > headerSize {
		out[headerSize] ^= 0xff
	}
	return out
}
