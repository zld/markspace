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
	// LossStartSegment restricts loss to packets whose custom payload segment
	// index is >= this value, so earlier segments stay loss-free and can anchor
	// a checkpoint while later segments keep gaps for resume to recover.
	// Zero applies loss to every segment.
	LossStartSegment uint32 `json:"loss_start_segment"`
}

// DefaultSimConfig returns a mild, reproducible impairment mix.
func DefaultSimConfig(seed uint64) SimConfig {
	return SimConfig{
		Seed: seed, LossRate: 8, DuplicateRate: 10,
		ReorderRate: 20, ReorderWindow: 4,
	}
}

// DemoSimConfig returns the acceptance-run impairment mix: segments 0 and 1
// remain complete so an interruption can anchor them, while the final segment
// experiences loss, duplication and reordering for resume to close.
func DemoSimConfig(seed uint64, lastSegment uint32) SimConfig {
	return SimConfig{
		Seed: seed, LossRate: 35, DuplicateRate: 15,
		ReorderRate: 25, ReorderWindow: 3,
		LossStartSegment: lastSegment,
	}
}

// simulate applies loss, duplication and reordering deterministically. The
// returned stream preserves packet sequence metadata; only delivery order and
// presence change. No media bytes are invented.
func simulate(packets []*rtp.Packet, cfg SimConfig) []*rtp.Packet {
	// One RNG indexed by global packet position for loss/duplication, so the
	// decisions for a given sequence number are identical on every run.
	lossRNG := mathrand.New(mathrand.NewPCG(cfg.Seed, cfg.Seed^0x9E3779B97F4A7C15))
	var stream []*rtp.Packet
	for _, p := range packets {
		// Each packet draws the same fixed number of values regardless of
		// whether it is dropped, keeping first run and resume in lockstep.
		lossRoll := lossRNG.IntN(100)
		dupRoll := lossRNG.IntN(100)
		eligible := lossEligible(p, cfg.LossStartSegment)
		if eligible && lossRoll < clampPct(cfg.LossRate) {
			continue // dropped: will surface as a reassembly gap
		}
		stream = append(stream, clonePacket(p))
		if dupRoll < clampPct(cfg.DuplicateRate) {
			stream = append(stream, clonePacket(p)) // duplicate
		}
	}
	if cfg.ReorderRate > 0 && cfg.ReorderWindow > 0 {
		// A separate RNG makes reordering independent of the loss/dup draws.
		reorderRNG := mathrand.New(mathrand.NewPCG(cfg.Seed^0x517CC1B727220A95, cfg.Seed))
		for i := 1; i < len(stream); i++ {
			if reorderRNG.IntN(100) < clampPct(cfg.ReorderRate) {
				back := 1 + reorderRNG.IntN(cfg.ReorderWindow)
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

func clampPct(rate int) int {
	if rate < 0 {
		return 0
	}
	if rate > 100 {
		return 100
	}
	return rate
}

// lossEligible reports whether a packet belongs to a segment that the config
// allows loss on. When LossStartSegment is zero all packets are eligible.
func lossEligible(p *rtp.Packet, start uint32) bool {
	if start == 0 {
		return true
	}
	h, err := decodeHeader(p.Payload)
	if err != nil {
		return true
	}
	return h.segment >= start
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
