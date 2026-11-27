package rtp

import (
	"github.com/pion/rtp"

	"mediakernel/internal/clock"
)

// GeneratePCMPackets builds deterministic LPCM RTP packets for tests and
// the demo. Timestamps advance by samplesPerPacket at clockRate; sequence
// numbers and timestamps are independent so wrap can be exercised.
func GeneratePCMPackets(ssrc uint32, clockRate uint32, packets, samplesPerPacket int) ([][]byte, error) {
	if packets <= 0 || samplesPerPacket <= 0 {
		return nil, clock.NewError("rtp", clock.CatInvalidParam,
			"packets and samples per packet must be positive")
	}
	out := make([][]byte, 0, packets)
	for i := 0; i < packets; i++ {
		// Two bytes of payload per sample (16-bit PCM), deterministic bytes.
		payload := make([]byte, samplesPerPacket*2)
		for s := range payload {
			payload[s] = byte((i*31 + s) & 0xFF)
		}
		pkt := &rtp.Packet{
			Header: rtp.Header{
				Version:        2,
				PayloadType:    96,
				SequenceNumber: uint16(i),
				Timestamp:      uint32(i * samplesPerPacket),
				SSRC:           ssrc,
				Marker:         i == packets-1,
			},
			Payload: payload,
		}
		raw, err := pkt.Marshal()
		if err != nil {
			return nil, clock.NewError("rtp", clock.CatBadLength,
				"marshal packet %d failed", i).Wrap(err)
		}
		out = append(out, raw)
	}
	return out, nil
}

// Reorder swaps two packet positions in arrival order, simulating network
// reordering without changing wire sequence numbers or timestamps.
func Reorder(packets [][]byte, i, j int) [][]byte {
	out := make([][]byte, len(packets))
	copy(out, packets)
	out[i], out[j] = out[j], out[i]
	return out
}
