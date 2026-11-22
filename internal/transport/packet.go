// Package transport normalizes RTP packet streams: deterministic 16-bit
// sequence unwrap, duplicate suppression, and jitter buffering with bounded
// loss recovery. Missing packets are recorded as gaps, never fabricated.
package transport

import (
	"fmt"

	"github.com/pion/rtp"
)

// MaxWindow bounds the jitter buffer; configurable but capped.
const MaxWindow = 4096

// StreamPacket is the normalized packet used across the pipeline.
type StreamPacket struct {
	ExtSeq      uint64 // deterministically unwrapped sequence number
	Timestamp   uint32 // RTP timestamp (media clock ticks)
	SSRC        uint32
	PayloadSize int
	Late        bool // arrived out of order but still recoverable
}

// Unwrapper expands 16-bit RTP sequence numbers to a monotonic 64-bit
// space. The rule is deterministic: a forward jump of more than half the
// sequence space is treated as a backward wrap, and vice versa. Wrap
// decisions use the highest extended sequence seen so far, so a late
// packet from a previous cycle never corrupts the cycle counter.
type Unwrapper struct {
	cycles  uint64
	maxExt  uint64
	started bool
}

// Unwrap maps seq into extended space relative to previously seen packets.
func (u *Unwrapper) Unwrap(seq uint16) uint64 {
	s := uint64(seq)
	if !u.started {
		u.started = true
		u.maxExt = s
		return s
	}
	maxLow := u.maxExt & 0xffff
	cand := u.cycles | s
	switch {
	case s > maxLow && s-maxLow > 0x8000:
		// Large forward-looking jump: actually a packet from previous cycle.
		if cand >= 1<<16 {
			cand -= 1 << 16
		}
	case maxLow > s && maxLow-s > 0x8000:
		// Wrapped forward into the next cycle.
		cand += 1 << 16
	}
	if cand > u.maxExt {
		u.maxExt = cand
		u.cycles = cand &^ 0xffff
	}
	return cand
}

// ParseRTP decodes one datagram with pion/rtp.
func ParseRTP(raw []byte) (*rtp.Packet, error) {
	p := &rtp.Packet{}
	if err := p.Unmarshal(raw); err != nil {
		return nil, fmt.Errorf("transport: RTP parse: %w", err)
	}
	return p, nil
}

// Normalize converts a parsed RTP packet into pipeline form.
func Normalize(p *rtp.Packet, u *Unwrapper) StreamPacket {
	return StreamPacket{
		ExtSeq:      u.Unwrap(p.SequenceNumber),
		Timestamp:   p.Timestamp,
		SSRC:        p.SSRC,
		PayloadSize: len(p.Payload),
	}
}
