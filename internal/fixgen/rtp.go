package fixgen

import "github.com/pion/rtp/v2"

// RTPSpec describes one synthetic RTP packet in a capture sidecar.
type RTPSpec struct {
	Seq       uint16
	Timestamp uint32
	SSRC      uint32
	Payload   []byte
}

// BuildRTP renders a capture file: a sequence of length-prefixed RTP
// packets (2-byte big-endian length + marshalled packet).
func BuildRTP(pkts []RTPSpec) []byte {
	var out []byte
	for _, p := range pkts {
		pkt := rtp.Packet{
			Header: rtp.Header{
				Version:   2,
				SSRC:      p.SSRC,
				Timestamp: p.Timestamp,
				SequenceNumber: p.Seq,
			},
			Payload: p.Payload,
		}
		raw, err := pkt.Marshal()
		if err != nil {
			panic(err)
		}
		out = append(out, byte(len(raw)>>8), byte(len(raw)))
		out = append(out, raw...)
	}
	return out
}
