// Package rtp implements the transport-side jitter buffer, gap recovery and
// the restricted payload format carried between edge nodes.
package rtp

import (
	"crypto/sha256"
	"encoding/binary"
	"errors"
	"fmt"
)

// Restricted payload layout (all integers big-endian):
//
//	magic "MDP1" | version u8 | flags u8 | trackID u16 | frame u32 |
//	mediaTicks u64 | dataLen u16 | data | checksum[8]
//
// The checksum is the first 8 bytes of SHA-256 over the header and data. The
// format is deliberately small and closed: only the project's own generator
// produces it and the parser rejects anything else.
const (
	PayloadMagic      = "MDP1"
	PayloadVersion    = 1
	payloadHeaderSize = 22
	payloadSumSize    = 8
	// MaxPayloadData bounds a single fragment's data allocation.
	MaxPayloadData = 1024
)

// Payload flags.
const (
	FlagKeyframe uint8 = 1 << 0
	FlagAudio    uint8 = 1 << 1
	FlagMetadata uint8 = 1 << 2
	FlagFragment uint8 = 1 << 3
)

var (
	// ErrPayloadFormat reports a malformed restricted payload.
	ErrPayloadFormat = errors.New("rtp: malformed restricted payload")
	// ErrPayloadTooLarge reports data beyond MaxPayloadData.
	ErrPayloadTooLarge = errors.New("rtp: payload exceeds size limit")
)

// Payload is one fragment of one track sample.
type Payload struct {
	Flags      uint8
	TrackID    uint16
	Frame      uint32
	MediaTicks uint64
	Data       []byte
}

// Marshal encodes the payload with its integrity checksum.
func (p Payload) Marshal() ([]byte, error) {
	if len(p.Data) > MaxPayloadData {
		return nil, fmt.Errorf("%w: %d > %d", ErrPayloadTooLarge, len(p.Data), MaxPayloadData)
	}
	if p.TrackID == 0 {
		return nil, fmt.Errorf("%w: track id 0", ErrPayloadFormat)
	}
	buf := make([]byte, payloadHeaderSize+len(p.Data)+payloadSumSize)
	copy(buf[0:4], PayloadMagic)
	buf[4] = PayloadVersion
	buf[5] = p.Flags
	binary.BigEndian.PutUint16(buf[6:8], p.TrackID)
	binary.BigEndian.PutUint32(buf[8:12], p.Frame)
	binary.BigEndian.PutUint64(buf[12:20], p.MediaTicks)
	binary.BigEndian.PutUint16(buf[20:22], uint16(len(p.Data)))
	copy(buf[22:], p.Data)
	sum := sha256.Sum256(buf[:payloadHeaderSize+len(p.Data)])
	copy(buf[payloadHeaderSize+len(p.Data):], sum[:payloadSumSize])
	return buf, nil
}

// ParsePayload decodes and validates a restricted payload.
func ParsePayload(b []byte) (Payload, error) {
	var p Payload
	if len(b) < payloadHeaderSize+payloadSumSize {
		return p, fmt.Errorf("%w: short buffer %d", ErrPayloadFormat, len(b))
	}
	if string(b[0:4]) != PayloadMagic {
		return p, fmt.Errorf("%w: bad magic", ErrPayloadFormat)
	}
	if b[4] != PayloadVersion {
		return p, fmt.Errorf("%w: version %d", ErrPayloadFormat, b[4])
	}
	p.Flags = b[5]
	p.TrackID = binary.BigEndian.Uint16(b[6:8])
	p.Frame = binary.BigEndian.Uint32(b[8:12])
	p.MediaTicks = binary.BigEndian.Uint64(b[12:20])
	n := int(binary.BigEndian.Uint16(b[20:22]))
	if n > MaxPayloadData {
		return Payload{}, fmt.Errorf("%w: declared data %d", ErrPayloadTooLarge, n)
	}
	if len(b) != payloadHeaderSize+n+payloadSumSize {
		return Payload{}, fmt.Errorf("%w: length %d does not match declared %d", ErrPayloadFormat, len(b), n)
	}
	if p.TrackID == 0 {
		return Payload{}, fmt.Errorf("%w: track id 0", ErrPayloadFormat)
	}
	sum := sha256.Sum256(b[:payloadHeaderSize+n])
	if string(sum[:payloadSumSize]) != string(b[payloadHeaderSize+n:]) {
		return Payload{}, fmt.Errorf("%w: checksum mismatch", ErrPayloadFormat)
	}
	p.Data = append([]byte(nil), b[payloadHeaderSize:payloadHeaderSize+n]...)
	return p, nil
}

// Digest returns the SHA-256 of the payload data only, used to prove the
// retransmitted fragment is byte-identical to what the sender emitted.
func (p Payload) Digest() [32]byte {
	return sha256.Sum256(p.Data)
}
