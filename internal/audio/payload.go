package audio

import (
	"encoding/binary"
	"errors"
	"hash/crc32"
	"math"
)

var PayloadMagic = [4]byte{'M', 'P', 'A', 'U'}

const (
	PayloadVersion = 1
	headerSize     = 16
)

// EncodePayload builds the self-describing signed 16-bit mono test payload.
// Layout: magic(4) | version(1) | flags(1) | count(2) | rate(4) | CRC32(4) | samples.
func EncodePayload(samples []int16, sampleRate uint32) []byte {
	b := make([]byte, headerSize+2*len(samples))
	copy(b[0:4], PayloadMagic[:])
	b[4] = PayloadVersion
	b[5] = 0
	binary.LittleEndian.PutUint16(b[6:8], uint16(len(samples)))
	binary.LittleEndian.PutUint32(b[8:12], sampleRate)
	for i, s := range samples {
		binary.LittleEndian.PutUint16(b[headerSize+2*i:], uint16(s))
	}
	crc := crc32.ChecksumIEEE(b[headerSize:])
	binary.LittleEndian.PutUint32(b[12:16], crc)
	return b
}

// DecodePayload validates magic, length, sample count and sample CRC.
func DecodePayload(payload []byte) ([]int16, uint32, error) {
	if len(payload) < headerSize {
		return nil, 0, errors.New("audio payload shorter than header")
	}
	if payload[0] != PayloadMagic[0] || payload[1] != PayloadMagic[1] ||
		payload[2] != PayloadMagic[2] || payload[3] != PayloadMagic[3] {
		return nil, 0, errors.New("bad audio payload magic")
	}
	if payload[4] != PayloadVersion {
		return nil, 0, errors.New("unsupported audio payload version")
	}
	count := int(binary.LittleEndian.Uint16(payload[6:8]))
	sampleRate := binary.LittleEndian.Uint32(payload[8:12])
	wantLen := headerSize + 2*count
	if count == 0 || len(payload) != wantLen {
		return nil, sampleRate, errors.New("audio payload length/count mismatch")
	}
	wantCRC := binary.LittleEndian.Uint32(payload[12:16])
	if crc32.ChecksumIEEE(payload[headerSize:]) != wantCRC {
		return nil, sampleRate, errors.New("audio sample CRC mismatch")
	}
	samples := make([]int16, count)
	for i := range samples {
		samples[i] = int16(binary.LittleEndian.Uint16(payload[headerSize+2*i:]))
	}
	return samples, sampleRate, nil
}

// RMS returns the root-mean-square amplitude.
func RMS(samples []int16) float64 {
	if len(samples) == 0 {
		return 0
	}
	var sum float64
	for _, s := range samples {
		v := float64(s)
		sum += v * v
	}
	return math.Sqrt(sum / float64(len(samples)))
}
