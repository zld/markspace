package stream

import (
	"github.com/pion/rtp"

	"mediqc/internal/fault"
)

// RTPCodec carries chunks as RTP packets so segment payloads traverse
// the same packetisation a broadcast handoff would use.
type RTPCodec struct {
	seq  uint16
	ssrc uint32
}

// NewRTPCodec builds a codec with a deterministic SSRC.
func NewRTPCodec(ssrc uint32) *RTPCodec { return &RTPCodec{ssrc: ssrc} }

// Encode wraps payload in a marshalled RTP packet.
func (c *RTPCodec) Encode(payload []byte) ([]byte, error) {
	c.seq++
	pkt := &rtp.Packet{
		Header: rtp.Header{
			Version:        2,
			SequenceNumber: c.seq,
			Timestamp:      uint32(c.seq) * 3000,
			SSRC:           c.ssrc,
			Marker:         true,
			PayloadType:    96,
		},
		Payload: payload,
	}
	return pkt.Marshal()
}

// Decode validates and unwraps an RTP packet.
func (c *RTPCodec) Decode(raw []byte) ([]byte, error) {
	var pkt rtp.Packet
	if err := pkt.Unmarshal(raw); err != nil {
		return nil, fault.New(fault.CodeTruncatedInput, 0, -1,
			"rtp unmarshal: "+err.Error(), "re-deliver the affected packet")
	}
	if pkt.SSRC != c.ssrc {
		return nil, fault.New(fault.CodeCacheTamper, 0, -1,
			"unexpected SSRC on the wire", "isolate the foreign source")
	}
	return pkt.Payload, nil
}
