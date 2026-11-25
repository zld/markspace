package repair

import "github.com/pion/rtp"

// rtpPacket builds a minimal packet for repair tests.
func rtpPacket(ts uint32, seq uint16) *rtp.Packet {
	return &rtp.Packet{
		Header: rtp.Header{
			Version:        2,
			PayloadType:    96,
			SequenceNumber: seq,
			Timestamp:      ts,
			SSRC:           7,
			Marker:         true,
		},
		Payload: []byte{0x01, 0x02, 0x03, 0x04},
	}
}
