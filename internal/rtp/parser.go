// Package rtp contains the project-owned RTP parsing, reordering and frame
// reassembly logic. It deliberately does not reuse a third party RTP stack so
// that malformed input handling is fully under test here.
package rtp

import (
	"errors"
	"fmt"
)

// RTP fixed header size and common constants (RFC 3550).
const (
	FixedHeaderSize = 12
	rtpVersion      = 2
	maxCSRCCount    = 15
)

// Parse error sentinels. Callers count these with errors.Is and skip the
// offending packet; a parse error must never crash the pipeline.
var (
	ErrPacketTooLarge  = errors.New("rtp: packet exceeds maximum packet size")
	ErrTruncatedHeader = errors.New("rtp: truncated header")
	ErrBadVersion      = errors.New("rtp: unsupported RTP version")
	ErrIllegalPadding  = errors.New("rtp: illegal padding size")
	ErrExtOutOfBounds  = errors.New("rtp: extension length exceeds packet bounds")
)

// Header is the decoded RTP fixed header plus CSRC list and extension.
type Header struct {
	Version          uint8
	Padding          bool
	Extension        bool
	Marker           bool
	PayloadType      uint8
	SequenceNumber   uint16
	Timestamp        uint32
	SSRC             uint32
	CSRC             []uint32
	ExtensionProfile uint16
	ExtensionPayload []byte
}

// Packet is one successfully parsed RTP packet. Slices alias the input buffer,
// which is safe because fixture buffers stay alive for the whole run.
type Packet struct {
	Header      Header
	Payload     []byte
	PaddingSize int
}

// Parser decodes raw RTP datagrams while enforcing a maximum packet size.
type Parser struct {
	maxPacketSize int
}

// NewParser builds a Parser. maxPacketSize is validated by config before this
// point; defensive bounds are applied anyway.
func NewParser(maxPacketSize int) *Parser {
	if maxPacketSize < FixedHeaderSize {
		maxPacketSize = FixedHeaderSize
	}
	return &Parser{maxPacketSize: maxPacketSize}
}

// Parse decodes one datagram. On failure it returns a wrapped sentinel error
// and no packet, so the caller can count and safely skip it.
func (p *Parser) Parse(buf []byte) (*Packet, error) {
	n := len(buf)
	if n > p.maxPacketSize {
		return nil, fmt.Errorf("%w: %d > %d bytes", ErrPacketTooLarge, n, p.maxPacketSize)
	}
	if n < FixedHeaderSize {
		return nil, fmt.Errorf("%w: %d < %d bytes", ErrTruncatedHeader, n, FixedHeaderSize)
	}

	b0, b1 := buf[0], buf[1]
	hdr := Header{
		Version:        b0 >> 6,
		Padding:        b0&0x20 != 0,
		Extension:      b0&0x10 != 0,
		PayloadType:    b1 & 0x7f,
		Marker:         b1&0x80 != 0,
		SequenceNumber: uint16(buf[2])<<8 | uint16(buf[3]),
		Timestamp:      uint32(buf[4])<<24 | uint32(buf[5])<<16 |
			uint32(buf[6])<<8 | uint32(buf[7]),
		SSRC: uint32(buf[8])<<24 | uint32(buf[9])<<16 |
			uint32(buf[10])<<8 | uint32(buf[11]),
	}
	if hdr.Version != rtpVersion {
		return nil, fmt.Errorf("%w: got %d", ErrBadVersion, hdr.Version)
	}

	off := FixedHeaderSize
	cc := int(b0 & 0x0f)
	if cc > maxCSRCCount { // unreachable: field is 4 bits, kept for clarity
		return nil, fmt.Errorf("%w: CSRC count %d", ErrTruncatedHeader, cc)
	}
	if csrcEnd := off + 4*cc; csrcEnd > n {
		return nil, fmt.Errorf("%w: CSRC table needs %d bytes in %d",
			ErrTruncatedHeader, csrcEnd, n)
	} else if cc > 0 {
		hdr.CSRC = make([]uint32, cc)
		for i := 0; i < cc; i++ {
			hdr.CSRC[i] = uint32(buf[off])<<24 | uint32(buf[off+1])<<16 |
				uint32(buf[off+2])<<8 | uint32(buf[off+3])
			off += 4
		}
	}

	if hdr.Extension {
		if off+4 > n {
			return nil, fmt.Errorf("%w: extension header", ErrTruncatedHeader)
		}
		hdr.ExtensionProfile = uint16(buf[off])<<8 | uint16(buf[off+1])
		words := int(uint16(buf[off+2])<<8 | uint16(buf[off+3]))
		extEnd := off + 4 + 4*words
		if extEnd > n {
			return nil, fmt.Errorf("%w: header+%d extension bytes in %d",
				ErrExtOutOfBounds, 4+4*words, n)
		}
		hdr.ExtensionPayload = buf[off+4 : extEnd]
		off = extEnd
	}

	pkt := &Packet{Header: hdr}
	payloadEnd := n
	if hdr.Padding {
		if off >= n {
			return nil, fmt.Errorf("%w: no room for padding marker", ErrIllegalPadding)
		}
		pad := int(buf[n-1])
		if pad == 0 || pad > n-off {
			return nil, fmt.Errorf("%w: size %d in %d byte payload region",
				ErrIllegalPadding, pad, n-off)
		}
		pkt.PaddingSize = pad
		payloadEnd -= pad
	}
	if payloadEnd < off {
		return nil, fmt.Errorf("%w: padding consumes header bytes", ErrIllegalPadding)
	}
	pkt.Payload = buf[off:payloadEnd]
	return pkt, nil
}
