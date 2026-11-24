package fixture

import (
	"github.com/pion/rtp"
)

// RTPPacketSpec describes one synthetic RTP packet.
type RTPPacketSpec struct {
	Sequence   uint16
	Timestamp  uint32
	Marker     bool
	Payload    byte
	PayloadLen int
}

// RTPPacket builds and serializes one RTP packet using pion/rtp so the
// wire layout is guaranteed compatible with the production parser.
func RTPPacket(spec RTPPacketSpec) ([]byte, error) {
	pt := byte(96)
	body := make([]byte, spec.PayloadLen)
	for i := range body {
		body[i] = spec.Payload + byte(i%251)
	}
	pkt := &rtp.Packet{
		Header: rtp.Header{
			Version:        2,
			PayloadType:    pt,
			SequenceNumber: spec.Sequence,
			Timestamp:      spec.Timestamp,
			Marker:         spec.Marker,
			SSRC:           0x53535243,
		},
		Payload: body,
	}
	return pkt.Marshal()
}

// RTPStream assembles a serialized packet stream with the given clock
// rate and per-packet tick deltas. It emits one initial packet plus one
// packet per delta, so len(deltas)+1 packets are produced. Wrap points
// and VFR gaps are whatever the deltas encode, so callers stay in control.
func RTPStream(tickRate uint32, startTS uint32, deltas []uint32, keyEvery int) ([][]byte, error) {
	out := make([][]byte, 0, len(deltas)+1)
	ts := startTS
	for i := 0; i <= len(deltas); i++ {
		marker := keyEvery > 0 && i%keyEvery == 0
		raw, err := RTPPacket(RTPPacketSpec{
			Sequence:   uint16(i),
			Timestamp:  ts,
			Marker:     marker,
			Payload:    byte(i + 1),
			PayloadLen: 12,
		})
		if err != nil {
			return nil, err
		}
		out = append(out, raw)
		if i < len(deltas) {
			ts += deltas[i]
		}
	}
	_ = tickRate
	return out, nil
}
