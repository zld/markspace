// Package transport parses RTP packets and turns lossy, reordered edge
// node streams into deterministic, gap-aware sequences without ever
// fabricating media content.
package transport

import (
	"fmt"

	"github.com/pion/rtp"

	"mediadist/timeline"
)

// MaxPacketBytes bounds a single RTP datagram; larger inputs are rejected
// before allocation so malformed traffic cannot grow memory without limit.
const MaxPacketBytes = 9000

// DefaultReorderWindow is the maximum forward sequence gap the jitter
// buffer will hold open while waiting for late packets.
const DefaultReorderWindow = 64

// ParsePacket validates size and delegates RTP header parsing to pion.
func ParsePacket(data []byte) (*rtp.Packet, error) {
	if len(data) == 0 {
		return nil, timeline.NewError("RTP_MALFORMED", "empty packet", nil)
	}
	if len(data) > MaxPacketBytes {
		return nil, timeline.NewError("RTP_MALFORMED", "packet exceeds size limit",
			map[string]any{"bytes": len(data), "limit": MaxPacketBytes})
	}
	p := &rtp.Packet{}
	if err := p.Unmarshal(data); err != nil {
		return nil, timeline.NewError("RTP_MALFORMED", "rtp unmarshal failed",
			map[string]any{"error": err.Error()})
	}
	if p.SSRC == 0 {
		return nil, timeline.NewError("RTP_MALFORMED", "zero SSRC", nil)
	}
	return p, nil
}

// SeqDistance is the signed forward distance from a to b in 16-bit RTP
// sequence space, so wraparound (65535 -> 0) is reported as +1 rather
// than a large negative number.
func SeqDistance(a, b uint16) int16 {
	return int16(b - a)
}

// TimestampDistance is the signed forward distance between two 32-bit
// RTP timestamps.
func TimestampDistance(a, b uint32) int64 {
	return int64(int32(b - a))
}

// ExtSeq maps a wrapped 16-bit sequence number onto a monotonic 64-bit
// counter, given the extended value assigned to the stream start.
func ExtSeq(baseExt uint64, baseSeq, seq uint16) uint64 {
	return baseExt + uint64(uint16(seq-baseSeq))
}

// Stream identifies one RTP source and its media clock rate.
type Stream struct {
	SSRC      uint32
	ClockRate uint32
}

// Event converts a packet into a timeline event whose Time is the RTP
// timestamp expressed exactly as a rational number of milliseconds.
func (s Stream) Event(p *rtp.Packet, epoch int, extSeq uint64) (timeline.Event, error) {
	if p.SSRC != s.SSRC {
		return timeline.Event{}, timeline.NewError("RTP_SSRC_MISMATCH",
			"packet does not belong to stream",
			map[string]any{"want": s.SSRC, "got": p.SSRC})
	}
	if s.ClockRate == 0 {
		return timeline.Event{}, timeline.NewError("INVALID_TIMESCALE",
			"stream clock rate must be positive",
			map[string]any{"ssrc": s.SSRC})
	}
	t, err := timeline.NewRat(int64(p.Timestamp), int64(s.ClockRate))
	if err != nil {
		return timeline.Event{}, err
	}
	t, err = t.MulInt(1000)
	if err != nil {
		return timeline.Event{}, err
	}
	return timeline.Event{
		Epoch: epoch,
		Time:  t,
		Seq:   extSeq,
		ID:    fmt.Sprintf("rtp-%d-%d", s.SSRC, extSeq),
		Kind:  "rtp",
		Data:  append([]byte(nil), p.Payload...),
	}, nil
}
