package fixture

import (
	"encoding/binary"

	pion "github.com/pion/rtp"
)

// WriteCapture serializes RTP packets as repeated [4-byte BE length][packet].
func WriteCapture(pkts []*pion.Packet) ([]byte, error) {
	var out []byte
	for _, p := range pkts {
		raw, err := p.Marshal()
		if err != nil {
			return nil, err
		}
		out = append(out, u32(uint32(len(raw)))...)
		out = append(out, raw...)
	}
	return out, nil
}

// Packet builds an RTP packet with the essential fields.
func Packet(pt uint8, seq uint16, ts uint32, ssrc uint32, marker bool, payload []byte) *pion.Packet {
	return &pion.Packet{
		Header: pion.Header{
			Version:        2,
			PayloadType:    pt,
			SequenceNumber: seq,
			Timestamp:      ts,
			SSRC:           ssrc,
			Marker:         marker,
		},
		Payload: payload,
	}
}

// TruncateCapture removes the final packet but leaves its length prefix
// intact, so the RTP adapter sees a declared length extending past EOF and
// reports a truncated-frame error at the packet offset.
func TruncateCapture(data []byte) []byte {
	if len(data) < 8 {
		return data
	}
	lastLen := int(binary.BigEndian.Uint32(data[len(data)-8 : len(data)-4]))
	cut := len(data) - 4 - lastLen // keep prefix through the last length field
	if cut < 4 {
		cut = 4
	}
	out := make([]byte, cut)
	copy(out, data[:cut])
	return out
}
