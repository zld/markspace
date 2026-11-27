// Package media: RTP time-base adaptation.
//
// pcr2 and transport timestamps arrive with 32-bit or 90 kHz clocks; RTP
// packets carry a 32-bit timestamp at an arbitrary (often 48 kHz) clock rate.
// This file pins both onto the same integer nanosecond timeline used by the
// rest of the pipeline, using pion/rtp for wire parsing.
package media

import (
	"github.com/pion/rtp"

	"example.com/assetpack/internal/clock"
	"example.com/assetpack/internal/fault"
)

// RTPClock is one RTP stream's timestamp base.
type RTPClock struct {
	Rate     uint32 // timestamp clock rate in Hz
	SSRC     uint32
	First    *uint32 // sequence/timestamp of the first accepted packet
	lastSeq  uint16
	haveSeq  bool
	base     uint64 // extended timestamp at zero
	lastTS   uint32
	wrap     uint64 // number of 2^32 wraps observed
	Packets  int64
	LostPkts int64
}

// NewRTPClock validates and constructs a clock.
func NewRTPClock(rate uint32) (*RTPClock, error) {
	if rate == 0 {
		return nil, fault.New(fault.StageAudio, fault.KindInvalidArg, "RTP clock rate must be positive")
	}
	return &RTPClock{Rate: rate}, nil
}

// TimestampResult is the resolved position of one RTP packet.
type TimestampResult struct {
	Extended uint64 `json:"extended"`
	Nanos    int64  `json:"nanos"`
	Sample   int64  `json:"sample"`
	Seq      uint16 `json:"seq"`
	Loss     int64  `json:"loss"`
}

// Resolve parses a marshalled RTP packet and places it on the timeline.
// Sequence-number gaps are reported but never re-ordered silently.
func (c *RTPClock) Resolve(raw []byte, sampleRate int64) (TimestampResult, error) {
	var pkt rtp.Packet
	if err := pkt.Unmarshal(raw); err != nil {
		return TimestampResult{}, fault.New(fault.StageAudio, fault.KindRTPInvalid,
			"RTP packet did not parse: %v", err)
	}
	return c.Accept(&pkt, sampleRate)
}

// Accept places an already-parsed packet on the timeline.
func (c *RTPClock) Accept(pkt *rtp.Packet, sampleRate int64) (TimestampResult, error) {
	if pkt == nil {
		return TimestampResult{}, fault.New(fault.StageAudio, fault.KindRTPInvalid, "nil RTP packet")
	}
	if c.Packets == 0 {
		if pkt.Version != 2 && pkt.Version != 0 {
			return TimestampResult{}, fault.New(fault.StageAudio, fault.KindRTPInvalid,
				"unsupported RTP version %d", pkt.Version)
		}
		c.SSRC = pkt.SSRC
		c.base = uint64(pkt.Timestamp)
		c.lastTS = pkt.Timestamp
		ts := pkt.Timestamp
		c.First = &ts
	}
	var loss int64
	seqDelta := int64(1)
	if c.haveSeq {
		seqDelta = (int64(pkt.SequenceNumber) - int64(c.lastSeq)) & 0xFFFF
		if seqDelta == 0 {
			return TimestampResult{}, fault.New(fault.StageAudio, fault.KindRTPInvalid,
				"duplicate RTP sequence number %d", pkt.SequenceNumber)
		}
		if seqDelta > 1 {
			loss = seqDelta - 1
			c.LostPkts += loss
		}
	}
	if pkt.Timestamp < c.lastTS {
		// A 32-bit RTP timestamp wrap is accepted only when it is a large
		// backwards step on an in-order packet; a small regression is an
		// out-of-order packet and is refused rather than folded into time.
		back := uint64(c.lastTS) - uint64(pkt.Timestamp)
		if seqDelta != 1 || back <= 1<<31 {
			return TimestampResult{}, fault.New(fault.StageAudio, fault.KindRTPGap,
				"RTP timestamp regressed by %d ticks out of order (seq %d after %d); resync is not automatic",
				back, pkt.SequenceNumber, c.lastSeq).Recover(false)
		}
		c.wrap++
	}
	c.lastTS = pkt.Timestamp
	c.lastSeq = pkt.SequenceNumber
	c.haveSeq = true
	c.Packets++

	extended := c.wrap<<32 + uint64(pkt.Timestamp)
	deltaTicks := int64(extended) - int64(c.base)
	nanos := deltaTicks * 1e9 / int64(c.Rate)
	var sample int64
	if sampleRate > 0 {
		sample = deltaTicks * sampleRate / int64(c.Rate)
	}
	return TimestampResult{
		Extended: extended, Nanos: nanos, Sample: sample,
		Seq: pkt.SequenceNumber, Loss: loss,
	}, nil
}

// MediaTime converts an RTP timestamp to the shared timeline.
func (c *RTPClock) MediaTime(ts uint32, sampleRate int64) clock.Position {
	extended := c.wrap<<32 + uint64(ts)
	nanos := (int64(extended) - int64(c.base)) * 1e9 / int64(c.Rate)
	sample := nanos * sampleRate / 1e9
	return clock.Position{Sample: sample, Nanos: nanos}
}
