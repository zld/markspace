package clock

import (
	"github.com/pion/rtp"
)

// buildClockPackets mirrors container sample DTS values as minimal RTP
// packets, exercising the pion/rtp timestamp path in tests.
func buildClockPackets(dts []uint64, dur uint32) [][]byte {
	pkts := make([][]byte, len(dts))
	for i, d := range dts {
		p := &rtp.Packet{
			Header: rtp.Header{
				Version:        2,
				PayloadType:    96,
				SequenceNumber: uint16(i),
				Timestamp:      uint32(d),
				SSRC:           1,
			},
			Payload: []byte{0x01},
		}
		buf, err := p.Marshal()
		if err != nil {
			panic(err)
		}
		pkts[i] = buf
	}
	return pkts
}
