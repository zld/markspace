package fanout

import (
	mathrand "math/rand/v2"

	"github.com/pion/rtp"
)

// SimConfig drives the deterministic local network simulation.
type SimConfig struct {
	Seed          uint64 `json:"seed"`
	LossRate      int    `json:"loss_rate_percent"`
	DuplicateRate int    `json:"duplicate_rate_percent"`
	ReorderRate   int    `json:"reorder_rate_percent"`
	// ReorderWindow bounds how far a packet can be delayed.
	ReorderWindow int `json:"reorder_window"`
}

// DefaultSimConfig returns a mild, reproducible impairment mix.
func DefaultSimConfig(seed uint64) SimConfig {
	return SimConfig{
		Seed: seed, LossRate: 8, DuplicateRate: 10,
		ReorderRate: 20, ReorderWindow: 4,
	}
}

// simulate applies loss, duplication and reordering deterministically. The
// returned stream preserves packet sequence metadata; only delivery order and
// presence change. No media bytes are invented.
func simulate(packets []*rtp.Packet, cfg SimConfig) []*rtp.Packet {
	rng := mathrand.New(mathrand.NewPCG(cfg.Seed, cfg.Seed^0x9E3779B97F4A7C15))
	var stream []*rtp.Packet
	for _, p := range packets {
		if pct(rng, cfg.LossRate) {
			continue // dropped: will surface as a reassembly gap
		}
		stream = append(stream, clonePacket(p))
		if pct(rng, cfg.DuplicateRate) {
			stream = append(stream, clonePacket(p)) // duplicate
		}
	}
	if cfg.ReorderRate > 0 && cfg.ReorderWindow > 0 {
		// Adjacent bounded swaps keep the reordering local and reversible.
		for i := 1; i < len(stream); i++ {
			if pct(rng, cfg.ReorderRate) {
				back := 1 + rng.IntN(cfg.ReorderWindow)
				j := i - back
				if j < 0 {
					j = 0
				}
				stream[i], stream[j] = stream[j], stream[i]
			}
		}
	}
	return stream
}

func pct(rng *mathrand.Rand, rate int) bool {
	if rate <= 0 {
		return false
	}
	if rate >= 100 {
		return true
	}
	return rng.IntN(100) < rate
}

func clonePacket(p *rtp.Packet) *rtp.Packet {
	raw, err := p.Marshal()
	if err != nil {
		// Marshal of our own packets cannot fail; keep the original as a guard.
		return p
	}
	cp := &rtp.Packet{}
	if err := cp.Unmarshal(raw); err != nil {
		return p
	}
	return cp
}
