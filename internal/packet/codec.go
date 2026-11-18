// Package packet defines the fixed-size voice frame format used by the
// jitterlab receiver chain.
//
// Wire format (big endian, 24 bytes of header followed by 8 bytes of payload):
//
//	offset  size  field
//	0       1     version (must be Version)
//	1       1     flags (reserved, must be zero)
//	2       2     header length (HeaderSize)
//	4       5     extended sequence number (40 bit, wraps at 2^40)
//	9       4     sample time (milliseconds)
//	13      4     send time (nanoseconds, relative to the sender epoch)
//	17      1     payload length (PayloadSize)
//	18      2     reserved (zero)
//	20      4     CRC-32 (IEEE) over header bytes 0..19 and the payload
//	24      8     payload
//
// Only the sequence number changes on retransmission; sample time, send
// time and payload stay stable so duplicates can be recognised.
package packet

import (
	"encoding/binary"
	"errors"
	"fmt"
	"hash/crc32"
)

const (
	// Version is the only protocol version understood by this build.
	Version = 1
	// HeaderSize is the fixed header length in bytes.
	HeaderSize = 24
	// PayloadSize is the fixed audio payload length in bytes.
	PayloadSize = 8
	// MaxPacketSize is the largest datagram the receiver will allocate for.
	MaxPacketSize = HeaderSize + PayloadSize
)

// Decode errors. Callers count these separately; nothing may be recovered
// from a packet that returns one of them.
var (
	ErrTruncated       = errors.New("packet: truncated frame")
	ErrUnknownVersion  = errors.New("packet: unknown version")
	ErrBadLength       = errors.New("packet: bad length field")
	ErrChecksum        = errors.New("packet: checksum mismatch")
	ErrPayloadMismatch = errors.New("packet: payload does not match header")
)

var crcTable = crc32.MakeTable(crc32.IEEE)

// Frame is a decoded, checksum-verified voice frame.
type Frame struct {
	Version    uint8
	Seq        uint64
	SampleTime uint32
	SendTime   uint32
	Payload    [PayloadSize]byte
}

// Encode serialises f into a fresh datagram. SampleTime and SendTime are
// truncated to 32 bits by design; the sequence number is masked to 40 bits.
func Encode(f Frame) []byte {
	buf := make([]byte, MaxPacketSize)
	buf[0] = Version
	buf[1] = 0
	binary.BigEndian.PutUint16(buf[2:4], HeaderSize)
	seq := MaskSeq(f.Seq)
	buf[4], buf[5], buf[6], buf[7], buf[8] = byte(seq>>32), byte(seq>>24), byte(seq>>16), byte(seq>>8), byte(seq)
	binary.BigEndian.PutUint32(buf[9:13], f.SampleTime)
	binary.BigEndian.PutUint32(buf[13:17], f.SendTime)
	buf[17] = PayloadSize
	copy(buf[HeaderSize:], f.Payload[:])
	binary.BigEndian.PutUint32(buf[20:24], checksum(buf))
	return buf
}

// Decode validates and parses a datagram. It returns ErrTruncated for
// short buffers, ErrUnknownVersion for unknown protocol versions and
// ErrChecksum for corrupted headers or payloads.
func Decode(buf []byte) (Frame, error) {
	if len(buf) < MaxPacketSize {
		return Frame{}, fmt.Errorf("%w: got %d bytes, need %d", ErrTruncated, len(buf), MaxPacketSize)
	}
	if buf[0] != Version {
		return Frame{}, fmt.Errorf("%w: %d", ErrUnknownVersion, buf[0])
	}
	if hl := binary.BigEndian.Uint16(buf[2:4]); hl != HeaderSize {
		return Frame{}, fmt.Errorf("%w: header length %d", ErrBadLength, hl)
	}
	if pl := buf[17]; pl != PayloadSize {
		return Frame{}, fmt.Errorf("%w: payload length %d", ErrBadLength, pl)
	}
	want := binary.BigEndian.Uint32(buf[20:24])
	if got := checksum(buf[:MaxPacketSize]); got != want {
		return Frame{}, fmt.Errorf("%w: got %08x want %08x", ErrChecksum, got, want)
	}
	var f Frame
	f.Version = buf[0]
	f.Seq = uint64(buf[4])<<32 | uint64(buf[5])<<24 | uint64(buf[6])<<16 | uint64(buf[7])<<8 | uint64(buf[8])
	f.SampleTime = binary.BigEndian.Uint32(buf[9:13])
	f.SendTime = binary.BigEndian.Uint32(buf[13:17])
	copy(f.Payload[:], buf[HeaderSize:])
	return f, nil
}

// checksum computes CRC-32/IEEE over the frame with the checksum field
// zeroed, so encode and decode stay symmetric.
func checksum(buf []byte) uint32 {
	var scratch [MaxPacketSize]byte
	n := copy(scratch[:], buf)
	scratch[20], scratch[21], scratch[22], scratch[23] = 0, 0, 0, 0
	return crc32.Checksum(scratch[:n], crcTable)
}

// MaskSeq reduces a sequence value to the 40-bit wire range.
func MaskSeq(seq uint64) uint64 {
	return seq & (1<<40 - 1)
}
