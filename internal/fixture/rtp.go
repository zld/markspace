package fixture

import (
	"github.com/pion/rtp"
)

// RTPStream is a synthetic RTP stream used to validate the event time base.
type RTPStream struct {
	Packets   []*rtp.Packet
	ClockRate uint32 // RTP timestamp units per second
	StartNS   int64  // media time of the first packet in nanoseconds
}

// DefaultRTPStream builds a 90 kHz video stream of 10 packets, 3600 ticks
// (40 ms) apart, so the RTP timestamps can be converted to nanoseconds and
// cross-checked against the Matroska block times.
func DefaultRTPStream() *RTPStream {
	const clockRate = 90000
	const step = 3600 // 40 ms at 90 kHz
	s := &RTPStream{ClockRate: clockRate}
	for i := uint16(0); i < 10; i++ {
		p := &rtp.Packet{
			Header: rtp.Header{
				Version:        2,
				PayloadType:    96,
				SequenceNumber: 1000 + i,
				Timestamp:      uint32(i) * step,
				SSRC:           0x11223344,
				Marker:         i == 9,
			},
			Payload: mediaFrame(int(i), 32),
		}
		s.Packets = append(s.Packets, p)
	}
	return s
}

// Marshal serializes every packet to bytes, preserving order, so a consumer can
// round-trip them through rtp.Packet.Unmarshal.
func (s *RTPStream) Marshal() ([][]byte, error) {
	out := make([][]byte, 0, len(s.Packets))
	for _, p := range s.Packets {
		b, err := p.Marshal()
		if err != nil {
			return nil, err
		}
		out = append(out, b)
	}
	return out, nil
}

// TimestampNS converts an RTP timestamp difference from the first packet into
// nanoseconds at the stream's clock rate.
func (s *RTPStream) TimestampNS(ts uint32) int64 {
	delta := int64(ts) - int64(s.Packets[0].Timestamp)
	return s.StartNS + delta*int64(nanosPerSecond)/int64(s.ClockRate)
}

const nanosPerSecond = 1_000_000_000
