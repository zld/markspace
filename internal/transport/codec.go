package transport

import (
	"errors"

	"github.com/pion/rtp"
)

// errBadPayload signals an RTP payload that is empty, oversized or not a
// whole number of 188-byte TS packets.
var errBadPayload = errors.New("transport: RTP payload must be 1..7 whole TS packets")

func rtpPacket(ssrc uint32, payloadType uint8, seq, timestamp uint32, marker bool, payload []byte) *rtp.Packet {
	return &rtp.Packet{
		Header: rtp.Header{
			Version:        2,
			PayloadType:    payloadType,
			SequenceNumber: uint16(seq),
			Timestamp:      timestamp,
			SSRC:           ssrc,
			Marker:         marker,
		},
		Payload: append([]byte(nil), payload...),
	}
}

// Decode parses one RTP packet from wire bytes using pion/rtp.
func Decode(wire []byte) (*rtp.Packet, error) {
	pkt := &rtp.Packet{}
	if err := pkt.Unmarshal(wire); err != nil {
		return nil, err
	}
	return pkt, nil
}
