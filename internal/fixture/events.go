package fixture

import (
	"encoding/hex"
	"fmt"

	"github.com/pion/rtp"
)

// BlockTimesNS are the four block instants (two tracks x two clusters).
var BlockTimesNS = []int64{0, 0, 500_000_000, 500_000_000}

// BlockTracks are the track numbers corresponding to BlockTimesNS.
var BlockTracks = []uint64{1, 2, 1, 2}

const (
	ssrcTrack1 = 11111
	ssrcTrack2 = 22222
)

// BuildEventsJSONL emits four event records (one per block), two SSRC
// mappings and four RTP packet records whose timestamps are converted from a
// 48 kHz RTP clock. Small event-time residuals stay inside the 1 ms tolerance.
func BuildEventsJSONL() ([]byte, error) {
	var out []byte
	residual := []int64{100, -200, 250, -150}
	for i := range BlockTimesNS {
		line := fmt.Sprintf(
			`{"kind":"event","at_ns":%d,"track":%d,"label":"block-%d"}`+"\n",
			BlockTimesNS[i]+residual[i], BlockTracks[i], i)
		out = append(out, line...)
	}
	out = append(out,
		fmt.Sprintf(`{"kind":"ssrc_map","ssrc":%d,"track":1,"clock_rate":48000}`+"\n", ssrcTrack1)...)
	out = append(out,
		fmt.Sprintf(`{"kind":"ssrc_map","ssrc":%d,"track":2,"clock_rate":48000}`+"\n", ssrcTrack2)...)

	for i := range BlockTimesNS {
		ssrc := ssrcTrack1
		if BlockTracks[i] == 2 {
			ssrc = ssrcTrack2
		}
		// RTP timestamp at 48 kHz ticks for the block instant.
		rtpTS := uint32(BlockTimesNS[i] / (1_000_000_000 / SampleRate))
		pkt := &rtp.Packet{
			Header: rtp.Header{
				Version:        2,
				SSRC:           uint32(ssrc),
				Timestamp:      rtpTS,
				SequenceNumber: uint16(i + 1),
				PayloadType:    97,
			},
			Payload: []byte{0x01, 0x02, 0x03, 0x04},
		}
		raw, err := pkt.Marshal()
		if err != nil {
			return nil, err
		}
		line := fmt.Sprintf(
			`{"kind":"rtp","at_ns":%d,"ssrc":%d,"rtp_ts":%d,"seq":%d,"packet_hex":"%s"}`+"\n",
			BlockTimesNS[i], ssrc, rtpTS, i+1, hex.EncodeToString(raw))
		out = append(out, line...)
	}
	return out, nil
}
