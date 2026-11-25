package pipeline

import "github.com/pion/rtp"

// newRTPPacket builds a minimal marked RTP packet for engine fault tests.
func newRTPPacket(ts uint32, seq uint16) *rtp.Packet {
	return &rtp.Packet{
		Header: rtp.Header{
			Version:        2,
			PayloadType:    96,
			SequenceNumber: seq,
			Timestamp:      ts,
			SSRC:           1,
			Marker:         true,
		},
		Payload: []byte{0x10, 0x20, 0x30, 0x40},
	}
}
