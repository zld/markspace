package rtp

import (
	"errors"
	"fmt"

	pionrtp "github.com/pion/rtp"
)

// Wire limits for the project's RTP profile.
const (
	// MaxWirePacket bounds one marshalled RTP packet.
	MaxWirePacket = 1500
	// WirePayloadType is the only payload type the profile carries.
	WirePayloadType = 96
	// WireSSRC is the fixed synchronization source of generated streams.
	WireSSRC = 0x4d445031
)

var (
	// ErrWireFormat reports an RTP packet outside the project profile.
	ErrWireFormat = errors.New("rtp: packet outside wire profile")
)

// ToWire marshals a Packet as an RFC 3550 RTP packet using pion/rtp.
func ToWire(p Packet) ([]byte, error) {
	if len(p.Payload) == 0 || len(p.Payload) > MaxWirePacket {
		return nil, fmt.Errorf("%w: payload %d bytes", ErrWireFormat, len(p.Payload))
	}
	hdr := pionrtp.Packet{
		Header: pionrtp.Header{
			Version:        2,
			PayloadType:    WirePayloadType,
			SequenceNumber: p.Seq,
			Timestamp:      p.TS,
			SSRC:           p.SSRC,
			Marker:         false,
		},
		Payload: p.Payload,
	}
	buf, err := hdr.Marshal()
	if err != nil {
		return nil, err
	}
	if len(buf) > MaxWirePacket+64 {
		return nil, fmt.Errorf("%w: wire packet %d bytes", ErrWireFormat, len(buf))
	}
	return buf, nil
}

// FromWire parses an RFC 3550 packet and enforces the profile: version 2, the
// platform payload type, no padding, no header extensions, one CSRC at most.
func FromWire(b []byte) (Packet, error) {
	if len(b) == 0 || len(b) > MaxWirePacket+64 {
		return Packet{}, fmt.Errorf("%w: length %d", ErrWireFormat, len(b))
	}
	var pp pionrtp.Packet
	if err := pp.Unmarshal(b); err != nil {
		return Packet{}, fmt.Errorf("%w: %v", ErrWireFormat, err)
	}
	if pp.Version != 2 || pp.PayloadType != WirePayloadType {
		return Packet{}, fmt.Errorf("%w: version %d pt %d", ErrWireFormat, pp.Version, pp.PayloadType)
	}
	if pp.Padding || len(pp.CSRC) > 0 || len(pp.Header.Extensions) > 0 {
		return Packet{}, fmt.Errorf("%w: padding/extension/csrc not supported", ErrWireFormat)
	}
	if len(pp.Payload) == 0 || len(pp.Payload) > MaxWirePacket {
		return Packet{}, fmt.Errorf("%w: payload %d bytes", ErrWireFormat, len(pp.Payload))
	}
	return Packet{Seq: pp.SequenceNumber, TS: pp.Timestamp, SSRC: pp.SSRC, Payload: append([]byte(nil), pp.Payload...)}, nil
}
