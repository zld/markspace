package fixture

import (
	"math/rand"

	"mediarc/internal/model"
)

func buildRTPPackets(opt Options, rng *rand.Rand) []model.RTPPacket {
	n := opt.PacketCount
	if n < 16 {
		n = 16
	}
	lossCount := opt.LossPackets
	if lossCount < 0 {
		lossCount = 0
	}
	if lossCount > n/3 {
		lossCount = n / 3
	}

	lost := map[int]bool{}
	burstLen := lossCount / 2
	if burstLen > 0 {
		start := 6 + int(opt.Seed%7)
		if start+burstLen >= n-4 {
			start = n/3 - burstLen
		}
		for i := 0; i < burstLen; i++ {
			lost[start+i] = true
		}
	}
	for len(lost) < lossCount {
		idx := rng.Intn(n)
		if !lost[idx] {
			lost[idx] = true
		}
	}

	logical := make([]model.RTPPacket, 0, n)
	const seqStart = uint16(65520)
	for i := 0; i < n; i++ {
		p := model.RTPPacket{
			Sequence:     seqStart + uint16(i),
			Timestamp:    uint32(i * 3000),
			SSRC:         0x4d524337,
			Marker:       i%10 == 9,
			Payload:      sequencePayload(opt.Seed, i),
			InjectedLoss: lost[i],
			SourceOffset: int64(65536 + i*128),
		}
		logical = append(logical, p)
	}

	aliveIndices := make([]int, 0, n-lossCount)
	for i := range logical {
		if !lost[i] {
			aliveIndices = append(aliveIndices, i)
		}
	}
	// Deterministic adjacent reordering.
	for s := 0; s < 6 && len(aliveIndices) > 4; s++ {
		at := 2 + rng.Intn(len(aliveIndices)-3)
		aliveIndices[at], aliveIndices[at+1] = aliveIndices[at+1], aliveIndices[at]
	}

	arrival := make([]model.RTPPacket, 0, len(aliveIndices)+lossCount+1)
	for _, idx := range aliveIndices {
		arrival = append(arrival, logical[idx])
	}
	if len(aliveIndices) > 1 {
		dup := logical[aliveIndices[1]]
		arrival = append(arrival, dup)
	}
	for i := range logical {
		if lost[i] {
			arrival = append(arrival, logical[i])
		}
	}
	return arrival
}

// LossOverWindowPackets builds a small contiguous sequence where all injected
// losses are forced past a four-packet recovery window by later progress.
func LossOverWindowPackets() ([]model.RTPPacket, []model.RTPPacket) {
	lost := []model.RTPPacket{
		{Sequence: 20, Timestamp: 60000, SSRC: 1, Payload: []byte("lost-20"), InjectedLoss: true, SourceOffset: 90020},
		{Sequence: 21, Timestamp: 63000, SSRC: 1, Payload: []byte("lost-21"), InjectedLoss: true, SourceOffset: 90021},
		{Sequence: 22, Timestamp: 66000, SSRC: 1, Payload: []byte("lost-22"), InjectedLoss: true, SourceOffset: 90022},
		{Sequence: 23, Timestamp: 69000, SSRC: 1, Payload: []byte("lost-23"), InjectedLoss: true, SourceOffset: 90023},
		{Sequence: 24, Timestamp: 72000, SSRC: 1, Payload: []byte("lost-24"), InjectedLoss: true, SourceOffset: 90024},
	}
	progress := []model.RTPPacket{
		{Sequence: 19, Timestamp: 57000, SSRC: 1, Payload: []byte("p19")},
		{Sequence: 25, Timestamp: 75000, SSRC: 1, Payload: []byte("p25")},
		{Sequence: 26, Timestamp: 78000, SSRC: 1, Payload: []byte("p26")},
		{Sequence: 27, Timestamp: 81000, SSRC: 1, Payload: []byte("p27")},
		{Sequence: 28, Timestamp: 84000, SSRC: 1, Payload: []byte("p28")},
		{Sequence: 29, Timestamp: 87000, SSRC: 1, Payload: []byte("p29")},
		{Sequence: 30, Timestamp: 90000, SSRC: 1, Payload: []byte("p30")},
	}
	return lost, progress
}
