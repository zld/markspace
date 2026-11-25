package fixture

import (
	"github.com/pion/rtp"
)

// marshalPacket builds an RTPv2 packet for a synthetic media sample.
func marshalPacket(ssrc uint32, seq uint16, ts uint32, payload []byte) []byte {
	pkt := rtp.Packet{
		Header: rtp.Header{
			Version:        2,
			PayloadType:    8,
			SequenceNumber: seq,
			Timestamp:      ts,
			SSRC:           ssrc,
		},
		Payload: payload,
	}
	raw, err := pkt.Marshal()
	if err != nil {
		panic(err) // deterministic in-process construction never fails
	}
	return raw
}
