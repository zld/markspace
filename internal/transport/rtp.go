// Package transport encapsulates MPEG-TS packets in RTP for cross-node
// transmission and validates the 16-bit RTP sequence number space. It uses
// github.com/pion/rtp for wire (un)marshalling.
package transport

import (
	"tsrepair/internal/mediaerr"
)

// MaxRTPPayload bounds a single RTP payload; TS-over-RTP carries a multiple
// of the 188-byte packet size.
const MaxRTPPayload = 7 * 188

// Encoder wraps TS byte payloads in RTP packets with an incrementing 16-bit
// sequence number.
type Encoder struct {
	SSRC        uint32
	PayloadType uint8
	nextSeq     uint16
}

// NewEncoder creates an RTP encoder. The sequence starts at zero.
func NewEncoder(ssrc uint32, payloadType uint8) *Encoder {
	return &Encoder{SSRC: ssrc, PayloadType: payloadType}
}

// EncodePacket wraps one TS packet (or a multiple-of-188 payload) in an RTP
// packet and returns its marshalled bytes. The marker is set on the caller's
// request for segment boundaries.
func (e *Encoder) EncodePacket(tsPayload []byte, marker bool, timestamp uint32) ([]byte, error) {
	if len(tsPayload) == 0 || len(tsPayload) > MaxRTPPayload || len(tsPayload)%188 != 0 {
		return nil, errBadPayload
	}
	seq := e.nextSeq
	e.nextSeq++ // wraps naturally at 16 bits
	pkt := rtpPacket(e.SSRC, e.PayloadType, uint32(seq), timestamp, marker, tsPayload)
	return pkt.Marshal()
}

// SeqChecker validates the RTP sequence number stream of one SSRC, mirroring
// the TS continuity rules on the 16-bit RTP sequence space.
type SeqChecker struct {
	last uint16
	have bool
	ec   *mediaerr.Collector
	ssrc uint32
}

// NewSeqChecker builds a checker recording into ec.
func NewSeqChecker(ssrc uint32, ec *mediaerr.Collector) *SeqChecker {
	return &SeqChecker{ec: ec, ssrc: ssrc}
}

// Observe inspects one sequence number and classifies gap, repeat and jump
// events with stable error codes. The first observation establishes the base.
func (c *SeqChecker) Observe(seq uint16, offset int64) {
	if !c.have {
		c.have = true
		c.last = seq
		return
	}
	delta := int(seq) - int(c.last)
	if delta < 0 {
		delta += 1 << 16
	}
	switch {
	case delta == 0:
		c.ec.Add(mediaerr.Recoverable(mediaerr.CodeRTPSeqRepeat, 0, offset,
			"RTP sequence number repeated", "drop duplicate RTP packet"))
	case delta == 1:
		// Continuous.
	default:
		if delta > (1 << 15) {
			c.ec.Add(mediaerr.Recoverable(mediaerr.CodeRTPSeqJump, 0, offset,
				"RTP sequence number large backward jump",
				"flush reassembly; resynchronise at this packet"))
		} else {
			c.ec.Add(mediaerr.Recoverable(mediaerr.CodeRTPSeqGap, 0, offset,
				"RTP sequence number gap: packets lost in transit",
				"flush reassembly; resume from this packet"))
		}
	}
	c.last = seq
}
