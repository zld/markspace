package fixture

import (
	"math/rand"

	"github.com/pion/rtp"
)

// RTPConfig controls the deterministic transport damage applied to a stream.
type RTPConfig struct {
	Seed          int64
	Packets       int
	SSRC          uint32
	ClockRate     uint32
	PayloadType   uint8
	LossRate      float64 // fraction of packets dropped permanently
	LateRate      float64 // fraction reordered but still within the playout window
	DuplicateRate float64 // fraction emitted twice (never counted as recovery)
	StartSeq      uint16
	BaseTimestamp uint32
	FrameStep     uint32 // timestamp increment per packet
}

// DefaultRTPConfig returns the acceptance-demo transport profile.
func DefaultRTPConfig(seed int64, packets int, loss float64) RTPConfig {
	return RTPConfig{
		Seed:          seed,
		Packets:       packets,
		SSRC:          0x4D465331, // 'MFS1'
		ClockRate:     90000,
		PayloadType:   96,
		LossRate:      loss * 0.7,
		LateRate:      loss * 0.3,
		DuplicateRate: 0.02,
		StartSeq:      1000,
		BaseTimestamp: 90000,
		FrameStep:     3750, // 25fps at 90kHz
	}
}

// RTPStream is the generated wire stream plus the ground-truth delivery map.
type RTPStream struct {
	Packets []*rtp.Packet
	// PermanentLost lists sequence numbers that never appear.
	PermanentLost []uint16
	// Late lists sequence numbers delivered out of order but in-window.
	Late []uint16
	// Duplicates lists sequence numbers emitted more than once.
	Duplicates []uint16
}

// GenerateRTP builds a deterministic damaged RTP stream.
func GenerateRTP(cfg RTPConfig) *RTPStream {
	rng := rand.New(rand.NewSource(cfg.Seed))
	stream := &RTPStream{}
	type event struct {
		pkt  *rtp.Packet
		seq  uint16
		late bool
		dup  bool
	}
	var events []event
	seq := cfg.StartSeq
	ts := cfg.BaseTimestamp
	for i := 0; i < cfg.Packets; i++ {
		curSeq := seq
		seq++
		roll := rng.Float64()
		switch {
		case roll < cfg.LossRate:
			stream.PermanentLost = append(stream.PermanentLost, curSeq)
			ts += cfg.FrameStep
			continue
		case roll < cfg.LossRate+cfg.LateRate:
			stream.Late = append(stream.Late, curSeq)
			events = append(events, event{makePacket(cfg, curSeq, ts, i), curSeq, true, false})
		case roll < cfg.LossRate+cfg.LateRate+cfg.DuplicateRate:
			stream.Duplicates = append(stream.Duplicates, curSeq)
			p := makePacket(cfg, curSeq, ts, i)
			events = append(events, event{p, curSeq, false, false}, event{p, curSeq, false, true})
		default:
			events = append(events, event{makePacket(cfg, curSeq, ts, i), curSeq, false, false})
		}
		ts += cfg.FrameStep
	}
	// Reorder late packets by swapping each one with its immediate neighbor.
	// This keeps recovery latency within one packet slot (~41.7 ms at 25fps)
	// while still producing genuine out-of-order, in-window arrivals.
	order := make([]int, len(events))
	for i := range order {
		order[i] = i
	}
	for idx, ev := range events {
		if ev.late && idx+1 < len(order) {
			j := idx + 1
			order[idx], order[j] = order[j], order[idx]
		}
	}
	for _, i := range order {
		stream.Packets = append(stream.Packets, events[i].pkt)
	}
	return stream
}

// GenerateRTPWindowLoss builds a stream where cfg.WindowSize consecutive
// sequence numbers are missing entirely: they exceed the recoverer window and
// must be reported unrecoverable rather than recovered.
func GenerateRTPWindowLoss(cfg RTPConfig, windowSize int) *RTPStream {
	stream := GenerateRTP(cfg)
	if windowSize <= 0 {
		return stream
	}
	// Remove every packet whose sequence falls in the first window gap.
	gapStart := cfg.StartSeq + uint16(cfg.Packets/3)
	gap := map[uint16]bool{}
	for k := 0; k < windowSize; k++ {
		s := gapStart + uint16(k)
		gap[s] = true
		stream.PermanentLost = append(stream.PermanentLost, s)
	}
	kept := stream.Packets[:0]
	for _, p := range stream.Packets {
		if !gap[p.SequenceNumber] {
			kept = append(kept, p)
		}
	}
	stream.Packets = kept
	return stream
}

func makePacket(cfg RTPConfig, seq uint16, ts uint32, idx int) *rtp.Packet {
	payload := SamplePayload(int(cfg.PayloadType), idx, int(seq), 48)
	return &rtp.Packet{
		Header: rtp.Header{
			Version:        2,
			PayloadType:    cfg.PayloadType,
			SequenceNumber: seq,
			Timestamp:      ts,
			SSRC:           cfg.SSRC,
			Marker:         true,
		},
		Payload: payload,
	}
}

// MarshalPackets serializes packets to RTP wire bytes for parser tests.
func MarshalPackets(stream *RTPStream) ([][]byte, error) {
	out := make([][]byte, 0, len(stream.Packets))
	for _, p := range stream.Packets {
		raw, err := p.Marshal()
		if err != nil {
			return nil, err
		}
		out = append(out, raw)
	}
	return out, nil
}
