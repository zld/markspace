// Package fixture generates deterministic in-memory RTP test packets.
package fixture

import (
	"fmt"

	"github.com/pion/rtp"
)

// Hard limits keep memory bounded regardless of CLI input.
const (
	MaxPackets       = 100000
	MaxPayloadBytes  = 1400
	DefaultClockRate = 90000
)

// Config describes a generated RTP stream.
type Config struct {
	Count        int    // number of packets to generate
	PayloadBytes int    // payload size per packet
	SeqStart     uint16 // first RTP sequence number
	SSRC         uint32 // stream SSRC
	ClockRate    uint32 // RTP clock rate, used for timestamp steps
}

// Validate enforces field-level limits.
func (c Config) Validate() error {
	if c.Count < 1 || c.Count > MaxPackets {
		return fmt.Errorf("packets: must be in [1,%d], got %d", MaxPackets, c.Count)
	}
	if c.PayloadBytes < 1 || c.PayloadBytes > MaxPayloadBytes {
		return fmt.Errorf("payload-bytes: must be in [1,%d], got %d", MaxPayloadBytes, c.PayloadBytes)
	}
	if c.ClockRate == 0 {
		return fmt.Errorf("clock rate must be positive")
	}
	return nil
}

// Generator produces packets one at a time; no full stream is materialized.
type Generator struct {
	cfg   Config
	sent  int
	seq   uint16
	ts    uint32
	tsInc uint32
}

// NewGenerator builds a generator. tsInc is the RTP timestamp step per
// packet, derived from the send interval by the caller.
func NewGenerator(cfg Config, tsInc uint32) *Generator {
	return &Generator{
		cfg:   cfg,
		seq:   cfg.SeqStart,
		ts:    0x1000, // non-zero start to exercise wrap handling less trivially
		tsInc: tsInc,
	}
}

// Next returns the next packet, or nil when the stream is exhausted.
// Payload bytes are a deterministic pattern derived from the sequence
// number so receivers can spot corruption without a shared secret.
func (g *Generator) Next() *rtp.Packet {
	if g.sent >= g.cfg.Count {
		return nil
	}
	payload := make([]byte, g.cfg.PayloadBytes)
	for i := range payload {
		payload[i] = byte(uint32(i)*31 + uint32(g.seq)*7)
	}
	pkt := &rtp.Packet{
		Header: rtp.Header{
			Version:        2,
			SequenceNumber: g.seq,
			Timestamp:      g.ts,
			SSRC:           g.cfg.SSRC,
			PayloadType:    96,
		},
		Payload: payload,
	}
	g.sent++
	g.seq++
	g.ts += g.tsInc
	return pkt
}
