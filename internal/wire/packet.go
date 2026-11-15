// Package wire defines the binary framing for synthetic audio stream
// packets and validates decoded fields.
package wire

import (
	"encoding/binary"
	"errors"
)

const (
	// Version is the only supported packet version.
	Version = 1
	// HeaderLen is the fixed header size in bytes.
	HeaderLen = 12
	// MaxPayload bounds the sample payload size in bytes.
	MaxPayload = 4096
)

var (
	ErrTooShort     = errors.New("wire: packet shorter than header")
	ErrBadVersion   = errors.New("wire: unsupported version")
	ErrBadStream    = errors.New("wire: unexpected stream id")
	ErrBadTimestamp = errors.New("wire: timestamp not aligned to frame clock")
	ErrBadPayload   = errors.New("wire: payload size invalid")
)

// Packet is a decoded synthetic audio frame packet.
type Packet struct {
	StreamID  uint32
	Seq       uint16
	Timestamp uint32 // send time in milliseconds on the frame clock
	Samples   []int16
}

// Encode serializes a packet: fixed header plus little-endian int16 samples.
func Encode(p *Packet) []byte {
	buf := make([]byte, HeaderLen+2*len(p.Samples))
	buf[0] = Version
	buf[1] = 0
	binary.BigEndian.PutUint32(buf[2:6], p.StreamID)
	binary.BigEndian.PutUint16(buf[6:8], p.Seq)
	binary.BigEndian.PutUint32(buf[8:12], p.Timestamp)
	for i, s := range p.Samples {
		binary.LittleEndian.PutUint16(buf[HeaderLen+2*i:], uint16(s))
	}
	return buf
}

// Decode parses and validates one datagram. streamID is the expected
// stream; frameMS (>0) is used to check timestamp alignment. A bad
// packet returns a classified error and never panics.
func Decode(data []byte, streamID uint32, frameMS int64) (*Packet, error) {
	if len(data) < HeaderLen {
		return nil, ErrTooShort
	}
	if data[0] != Version {
		return nil, ErrBadVersion
	}
	p := &Packet{
		StreamID:  binary.BigEndian.Uint32(data[2:6]),
		Seq:       binary.BigEndian.Uint16(data[6:8]),
		Timestamp: binary.BigEndian.Uint32(data[8:12]),
	}
	if p.StreamID != streamID {
		return nil, ErrBadStream
	}
	if frameMS > 0 && uint64(p.Timestamp)%uint64(frameMS) != 0 {
		return nil, ErrBadTimestamp
	}
	payload := data[HeaderLen:]
	if len(payload) == 0 || len(payload)%2 != 0 || len(payload) > MaxPayload {
		return nil, ErrBadPayload
	}
	p.Samples = make([]int16, len(payload)/2)
	for i := range p.Samples {
		p.Samples[i] = int16(binary.LittleEndian.Uint16(payload[2*i:]))
	}
	return p, nil
}

// Category maps a decode error to a stable counter label for reporting.
func Category(err error) string {
	switch {
	case errors.Is(err, ErrTooShort):
		return "too_short"
	case errors.Is(err, ErrBadVersion):
		return "bad_version"
	case errors.Is(err, ErrBadStream):
		return "bad_stream"
	case errors.Is(err, ErrBadTimestamp):
		return "bad_timestamp"
	case errors.Is(err, ErrBadPayload):
		return "bad_payload"
	default:
		return "unknown"
	}
}
