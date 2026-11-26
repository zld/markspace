package rtp

import (
	"fmt"

	pionrtp "github.com/pion/rtp"
)

// BuildPacket assembles a real RTP datagram with pion/rtp and returns it as a
// trace Packet, so fixtures and tests exercise the same parser the CLI uses.
func BuildPacket(source string, ssrc uint32, payloadType uint8, seq uint16, ts uint32, marker bool, payload []byte) (Packet, error) {
	h := pionrtp.Header{
		Version:        2,
		PayloadType:    payloadType,
		SequenceNumber: seq,
		Timestamp:      ts,
		SSRC:           ssrc,
		Marker:         marker,
	}
	pkt := pionrtp.Packet{Header: h, Payload: payload}
	buf := make([]byte, pkt.MarshalSize())
	n, err := pkt.MarshalTo(buf)
	if err != nil {
		return Packet{}, fmt.Errorf("build rtp packet: %w", err)
	}
	return Packet{
		Source:  source,
		SSRC:    ssrc,
		Payload: payloadType,
		Seq:     seq,
		TS:      ts,
		Marker:  marker,
		Raw:     buf[:n],
	}, nil
}

// Payload returns the RTP payload without the 12-byte fixed header. Traces in
// this project carry no header extension or padding, so the offset is fixed.
func (p Packet) PayloadBytes() ([]byte, error) {
	if len(p.Raw) < 12 {
		return nil, fmt.Errorf("rtp packet %d: %d bytes is shorter than a header", p.Index, len(p.Raw))
	}
	cc := int(p.Raw[0] & 0x0f)
	off := 12 + 4*cc
	if len(p.Raw) < off {
		return nil, fmt.Errorf("rtp packet %d: %d bytes is shorter than header with %d CSRCs", p.Index, len(p.Raw), cc)
	}
	return p.Raw[off:], nil
}
