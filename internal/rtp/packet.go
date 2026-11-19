// Package rtp contains a self-contained RTP packet parser, sequence
// reorderer and the counters shared by the TCP and UDP adapters.
package rtp

import (
	"encoding/binary"
	"errors"
)

// MaxPacketSize bounds every accepted RTP datagram, including the
// header. Packets larger than this are rejected as malformed instead
// of being allocated or processed.
const MaxPacketSize = 1500

const (
	fixedHeaderSize = 12
	rtpVersion      = 2
	csrcSize        = 4
	extHeaderSize   = 4
)

// Parse errors. They are returned wrapped; use errors.Is.
var (
	ErrPacketTooLarge  = errors.New("rtp: packet exceeds MaxPacketSize")
	ErrShortPacket     = errors.New("rtp: packet shorter than fixed header")
	ErrBadVersion      = errors.New("rtp: unsupported RTP version")
	ErrCSRCOverflow    = errors.New("rtp: CSRC list runs past packet end")
	ErrExtensionOverflow = errors.New("rtp: header extension runs past packet end")
	ErrPaddingOverflow = errors.New("rtp: padding larger than payload section")
	ErrZeroPadding     = errors.New("rtp: padding flag set but padding size is zero")
)

// Packet is the parsed view of one RTP datagram. Byte slices alias
// the input buffer, which must stay immutable while the packet is used.
type Packet struct {
	Version        uint8
	Padding        bool
	Extension      bool
	Marker         bool
	CSRCCount      uint8
	PayloadType    uint8
	SequenceNumber uint16
	Timestamp      uint32
	SSRC           uint32
	CSRC           []uint32
	// ExtensionProfile is zero when the extension bit is not set.
	ExtensionProfile uint16
	ExtensionPayload []byte
	// Payload excludes header, extension and padding bytes.
	Payload     []byte
	PaddingSize int
}

// Parse validates and decodes an RTP packet. It never panics on
// attacker-controlled input: every length is checked against the
// buffer before indexing, and every failure is returned as an error
// so the caller can account it in the malformed counter.
func Parse(buf []byte) (*Packet, error) {
	if len(buf) > MaxPacketSize {
		return nil, ErrPacketTooLarge
	}
	if len(buf) < fixedHeaderSize {
		return nil, ErrShortPacket
	}

	b0 := buf[0]
	version := b0 >> 6
	if version != rtpVersion {
		return nil, ErrBadVersion
	}

	p := &Packet{
		Version:        version,
		Padding:        b0&0x20 != 0,
		Extension:      b0&0x10 != 0,
		CSRCCount:      b0 & 0x0f,
		Marker:         buf[1]&0x80 != 0,
		PayloadType:    buf[1] & 0x7f,
		SequenceNumber: binary.BigEndian.Uint16(buf[2:4]),
		Timestamp:      binary.BigEndian.Uint32(buf[4:8]),
		SSRC:           binary.BigEndian.Uint32(buf[8:12]),
	}

	offset := fixedHeaderSize

	// CSRC list.
	if n := int(p.CSRCCount); n > 0 {
		end := offset + n*csrcSize
		if end > len(buf) {
			return nil, ErrCSRCOverflow
		}
		p.CSRC = make([]uint32, n)
		for i := 0; i < n; i++ {
			p.CSRC[i] = binary.BigEndian.Uint32(buf[offset : offset+csrcSize])
			offset += csrcSize
		}
	}

	// Header extension: 16-bit profile, 16-bit length in 32-bit words.
	if p.Extension {
		if offset+extHeaderSize > len(buf) {
			return nil, ErrExtensionOverflow
		}
		p.ExtensionProfile = binary.BigEndian.Uint16(buf[offset : offset+2])
		words := int(binary.BigEndian.Uint16(buf[offset+2 : offset+4]))
		offset += extHeaderSize
		extEnd := offset + words*4
		if extEnd > len(buf) {
			return nil, ErrExtensionOverflow
		}
		p.ExtensionPayload = buf[offset:extEnd]
		offset = extEnd
	}

	// Padding size lives in the last byte of the packet.
	trailer := 0
	if p.Padding {
		padSize := int(buf[len(buf)-1])
		if padSize == 0 {
			return nil, ErrZeroPadding
		}
		if padSize > len(buf)-offset {
			return nil, ErrPaddingOverflow
		}
		p.PaddingSize = padSize
		trailer = padSize
	}

	if offset > len(buf)-trailer {
		return nil, ErrShortPacket
	}
	p.Payload = buf[offset : len(buf)-trailer]
	return p, nil
}

// HeaderSize returns the serialized header length of a packet with
// the given CSRC count and extension words. It is used by the mock
// source and the parser tests to build well-formed datagrams.
func HeaderSize(csrcCount int, extWords int, hasExt bool) int {
	n := fixedHeaderSize + csrcCount*csrcSize
	if hasExt {
		n += extHeaderSize + extWords*4
	}
	return n
}
