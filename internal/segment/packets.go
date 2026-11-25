package segment

import (
	"github.com/pion/rtp"
)

// BuildPackets renders a deterministic RTP fragment stream. One packet per
// frame; the final packet of each frame carries the marker bit so fragment
// boundaries are explicit.
func BuildPackets(frameCount int, rtpStart uint32, rtpStep uint32, ssrc uint32) ([]Packet, error) {
	pkts := make([]Packet, 0, frameCount)
	offset := 0
	for i := 0; i < frameCount; i++ {
		pkt := &rtp.Packet{
			Header: rtp.Header{
				Version:        2,
				Marker:         true,
				PayloadType:    96,
				SequenceNumber: uint16(i),
				Timestamp:      rtpStart + uint32(i)*rtpStep,
				SSRC:           ssrc,
			},
			Payload: []byte{byte(i)},
		}
		raw, err := pkt.Marshal()
		if err != nil {
			return nil, err
		}
		pkts = append(pkts, Packet{Raw: raw, ByteOffset: int64(offset)})
		offset += len(raw)
	}
	return pkts, nil
}

// PacketTimestamps extracts the RTP timestamp sequence for cross-layer checks.
func PacketTimestamps(pkts []Packet) ([]uint32, error) {
	out := make([]uint32, 0, len(pkts))
	for _, p := range pkts {
		var pkt rtp.Packet
		if err := pkt.Unmarshal(p.Raw); err != nil {
			return nil, err
		}
		out = append(out, pkt.Timestamp)
	}
	return out, nil
}
