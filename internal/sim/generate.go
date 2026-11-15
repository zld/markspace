package sim

import (
	"math/rand"
	"sort"
	"time"

	"jbbench/internal/protocol"
)

// Generate builds the full impaired event stream for the configured sender.
// The returned slice is ordered by arrival time. Repeated calls with an
// identical Config produce byte-identical output.
func Generate(cfg Config) ([]Event, error) {
	if err := cfg.Validate(); err != nil {
		return nil, err
	}
	rng := rand.New(rand.NewSource(cfg.Seed))
	frameNS := protocol.FrameDurationNS()
	buf := make([]byte, protocol.MaxPacketSize)
	var events []Event

	lost := make(map[uint32]bool)
	for i := 0; i < cfg.Frames; {
		// Do not start a new burst directly after an existing loss run, or
		// the merged run would exceed the configured maximum burst length.
		if i > 0 && lost[uint32(i-1)] {
			i++
			continue
		}
		if cfg.LossRate > 0 && rng.Float64() < cfg.LossRate {
			burst := 1
			if cfg.LossBurst > 1 {
				burst = 1 + rng.Intn(cfg.LossBurst)
			}
			for b := 0; b < burst && i < cfg.Frames; b, i = b+1, i+1 {
				lost[uint32(i)] = true
				events = append(events, Event{
					FrameNo:  uint32(i),
					Seq:      uint16(i),
					Kind:     EventLost,
					ArriveNS: arrivalTime(i, frameNS, rng, cfg.JitterNS),
				})
			}
			continue
		}
		i++
	}

	for i := 0; i < cfg.Frames; i++ {
		no := uint32(i)
		if lost[no] {
			continue
		}
		pkt := makePacket(no, i)
		arrival := arrivalTime(i, frameNS, rng, cfg.JitterNS)
		if cfg.ReorderWindow > 0 && rng.Float64() < 0.10 {
			arrival += frameNS * time.Duration(1+rng.Intn(cfg.ReorderWindow))
		}
		kind, dg := impair(buf, pkt, rng, cfg)
		events = append(events, Event{
			FrameNo: no, Seq: uint16(i), ArriveNS: arrival,
			Kind: kind, Datagram: dg,
		})
		// Duplicates only replicate intact datagrams, mirroring real link
		// behavior where a duplicated copy is the same valid packet.
		if kind == EventOK && rng.Float64() < cfg.DuplicateRate {
			events = append(events, Event{
				FrameNo: no, Seq: uint16(i),
				ArriveNS: arrival + frameNS/4,
				Kind:     EventDuplicate,
				Datagram: append([]byte(nil), dg...),
			})
		}
	}

	// Half-interval ambiguity fixture: a packet whose seq is exactly 32768
	// ahead of a valid one must be rejected by the modular comparison.
	if cfg.Frames > 10 {
		pkt := makePacket(10, 10+32768)
		n := protocol.Encode(buf, pkt)
		events = append(events, Event{
			FrameNo: 10, Seq: uint16(10 + 32768),
			ArriveNS: arrivalTime(10, frameNS, rng, cfg.JitterNS),
			Kind:     EventAmbiguous, Datagram: append([]byte(nil), buf[:n]...),
		})
	}

	sort.SliceStable(events, func(a, b int) bool {
		return events[a].ArriveNS < events[b].ArriveNS
	})
	return events, nil
}

func arrivalTime(i int, frameNS time.Duration, rng *rand.Rand,
	jitter time.Duration) time.Duration {
	base := time.Duration(i) * frameNS
	if jitter <= 0 {
		return base
	}
	// Zero-mean uniform jitter in [-jitter/2, +jitter/2].
	return base + time.Duration(rng.Int63n(int64(jitter)))-jitter/2
}

func makePacket(no uint32, seqInt int) protocol.Packet {
	samples := make([]int16, protocol.SamplesPerFrame)
	for i := range samples {
		samples[i] = int16((int(no)*1103 + i*17) % 8192)
	}
	return protocol.Packet{
		Version: protocol.Version,
		Seq:     uint16(seqInt),
		FrameNo: no,
		SendNS:  uint64(no) * uint64(protocol.FrameDurationNS()),
		Samples: samples,
	}
}
