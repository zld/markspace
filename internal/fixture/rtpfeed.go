package fixture

import (
	"math/rand"

	"github.com/pion/rtp"
)

// FeedConfig controls a deterministic lossy RTP return-feed simulation.
type FeedConfig struct {
	Seed          int64
	Packets       int
	LossRate      float64 // fraction of packets initially missing
	JitterMs      int     // drives the maximum reorder radius
	Window        int     // receiver window the feed must respect
	DuplicateRate float64 // fraction of delivered packets repeated
	ClockRate     int64   // RTP clock used for timestamping
	// HardLossFraction is the share of missing packets that never return;
	// the rest arrive late inside the window and become recoveries.
	HardLossFraction float64
}

// SimulateFeed returns RTP datagrams in arrival order. Most "lost" packets are
// reinserted late within the window (recoverable reordering); a configured
// fraction are dropped for good or arrive beyond it (unrecoverable), and some
// deliveries are duplicated. The sequence is fully seed-determined.
func SimulateFeed(cfg FeedConfig) [][]byte {
	if cfg.HardLossFraction <= 0 {
		cfg.HardLossFraction = 0.08
	}
	if cfg.Window <= 0 {
		cfg.Window = 32
	}
	rng := rand.New(rand.NewSource(cfg.Seed))
	maxDelay := 1 + cfg.JitterMs/8
	if maxDelay < 1 {
		maxDelay = 1
	}
	if maxDelay > cfg.Window/2 {
		maxDelay = cfg.Window / 2
	}
	if maxDelay < 1 {
		maxDelay = 1
	}

	type datagram struct {
		slot int
		raw  []byte
	}
	var events []datagram
	for seq := 0; seq < cfg.Packets; seq++ {
		pkt := &rtp.Packet{
			Header: rtp.Header{
				Version: 2, PayloadType: 96,
				SequenceNumber: uint16(seq),
				Timestamp:      uint32(seq) * uint32Span(cfg.ClockRate),
			},
			Payload: []byte{byte(seq >> 8), byte(seq)},
		}
		raw, _ := pkt.Marshal()
		slot := seq
		missing := rng.Float64() < cfg.LossRate
		switch {
		case missing && rng.Float64() < cfg.HardLossFraction:
			continue // never delivered: unrecoverable
		case missing:
			// Late but inside the window: a recoverable reorder event.
			slot = seq + 1 + rng.Intn(maxDelay)
		}
		events = append(events, datagram{slot: slot, raw: raw})
		if rng.Float64() < cfg.DuplicateRate {
			events = append(events, datagram{slot: slot + 1, raw: raw})
		}
	}
	// Stable sort by arrival slot; equal slots keep generation order.
	for i := 1; i < len(events); i++ {
		for j := i; j > 0 && events[j-1].slot > events[j].slot; j-- {
			events[j-1], events[j] = events[j], events[j-1]
		}
	}
	out := make([][]byte, len(events))
	for i, e := range events {
		out[i] = e.raw
	}
	return out
}

func uint32Span(rate int64) uint32 {
	if rate <= 0 {
		return 1
	}
	// One packet per video frame at 24 fps on the RTP clock.
	span := rate / 24
	if span <= 0 {
		return 1
	}
	return uint32(span)
}
