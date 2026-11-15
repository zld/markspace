// Package sim generates deterministic synthetic packet streams with
// jitter, burst loss, reordering and duplicates from a fixed seed.
package sim

import (
	"math"
	"math/rand"
	"sort"

	"streamlab/internal/wire"
)

// Config describes one simulated network scenario.
type Config struct {
	Seed          int64
	Frames        int
	FrameMS       int64
	SampleRate    int // samples per second
	BaseDelayMS   float64
	JitterMS      float64
	LossBurst     int // max burst length; 0 disables loss
	DuplicateRate float64
	ReorderRate   float64
	StreamID      uint32
}

// Datagram is one scheduled packet on the virtual clock.
type Datagram struct {
	ArrivalMs int64
	Data      []byte
}

// SamplesPerFrame returns the frame size in samples.
func (c Config) SamplesPerFrame() int {
	return int(c.FrameMS) * c.SampleRate / 1000
}

// Generate builds the datagram list sorted by arrival time. The same
// seed and config always produce the same stream.
func Generate(cfg Config) []Datagram {
	rng := rand.New(rand.NewSource(cfg.Seed))
	spf := cfg.SamplesPerFrame()
	var out []Datagram

	emit := func(i int, extraDelay float64) {
		samples := make([]int16, spf)
		for j := range samples {
			t := float64(i*spf+j) / float64(cfg.SampleRate)
			samples[j] = int16(12000 * math.Sin(2*math.Pi*440*t))
		}
		pkt := wire.Packet{
			StreamID:  cfg.StreamID,
			Seq:       uint16(i),
			Timestamp: uint32(int64(i) * cfg.FrameMS),
			Samples:   samples,
		}
		delay := cfg.BaseDelayMS + cfg.JitterMS*rng.NormFloat64() + extraDelay
		if delay < 0 {
			delay = 0
		}
		out = append(out, Datagram{
			ArrivalMs: int64(i)*cfg.FrameMS + int64(math.Round(delay)),
			Data:      wire.Encode(&pkt),
		})
	}

	inBurst := 0
	for i := 0; i < cfg.Frames; i++ {
		if inBurst > 0 { // frame lost inside a burst
			inBurst--
			continue
		}
		if cfg.LossBurst > 0 && rng.Float64() < 0.012*float64(cfg.LossBurst) {
			inBurst = rng.Intn(cfg.LossBurst) // lose this frame plus followers
			continue
		}
		emit(i, 0)
		if rng.Float64() < cfg.DuplicateRate {
			emit(i, 1+rng.Float64()*cfg.JitterMS)
		}
	}

	// Reorder: swap arrival times of adjacent datagrams.
	for i := 0; i+1 < len(out); i++ {
		if rng.Float64() < cfg.ReorderRate {
			out[i].ArrivalMs, out[i+1].ArrivalMs = out[i+1].ArrivalMs, out[i].ArrivalMs
		}
	}
	sort.SliceStable(out, func(a, b int) bool {
		return out[a].ArrivalMs < out[b].ArrivalMs
	})
	return out
}
