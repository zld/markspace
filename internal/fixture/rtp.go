package fixture

import (
	"github.com/pion/rtp"

	"mediamux/media"
)

// buildRTP marshals a deterministic packet stream. Sequence numbers advance
// by one per packet; dropped packets simply do not appear; duplicated packets
// appear twice with identical bytes so dedup logic can be exercised.
func buildRTP(count int, seqStart uint16, tsStart uint32, payloadLen int, drop, dup map[uint16]bool) ([][]byte, error) {
	if count <= 0 || payloadLen <= 0 {
		return nil, media.NewError(media.CodeInput, media.PhaseTransport, "invalid rtp fixture shape").AtOffset(0)
	}
	out := make([][]byte, 0, count)
	for i := 0; i < count; i++ {
		seq := seqStart + uint16(i)
		if drop != nil && drop[seq] {
			continue
		}
		payload := make([]byte, payloadLen)
		for j := range payload {
			payload[j] = byte((i*7 + j*3 + int(seq)) % 251)
		}
		p := &rtp.Packet{
			Header: rtp.Header{
				Version: 2, PayloadType: 96, SequenceNumber: seq,
				Timestamp: tsStart + uint32(i)*3003, SSRC: 0x0badf00d,
				Marker: i == count-1,
			},
			Payload: payload,
		}
		buf, err := p.Marshal()
		if err != nil {
			return nil, media.NewError(media.CodeInternal, media.PhaseTransport, err.Error()).AtOffset(int64(i))
		}
		out = append(out, buf)
		if dup != nil && dup[seq] {
			out = append(out, append([]byte(nil), buf...))
		}
	}
	return out, nil
}
