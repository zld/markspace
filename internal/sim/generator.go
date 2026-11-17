// Package sim generates reproducible 48 kHz mono PCM16 frame streams and
// applies deterministic network impairments (jitter, loss, burst loss,
// reorder, duplication) so receive-side continuity can be checked offline.
package sim

import (
	"math"
	"math/rand"
	"sort"

	"jbcheck/internal/packet"
)

const (
	FrameSamples = 480 // 10 ms at 48 kHz
	SampleRate   = 48000
)

// Config describes one reproducible generated stream.
type Config struct {
	Seed      int64
	Frames    int
	Loss      float64 // independent per-frame loss probability
	Burst     float64 // probability that a loss extends into a burst
	Reorder   float64 // probability of a 5-20 ms extra delay
	Duplicate float64 // probability of emitting a duplicate copy
	JitterMs  float64 // uniform +/- jitter on arrival time
}

// Event is one datagram arrival in the generated trace.
type Event struct {
	ArrivalMs int64
	Data      []byte
}

// PCMFrame returns the 10 ms frame with the given index: a continuous-phase
// 440 Hz tone, fully determined by the frame index.
func PCMFrame(idx int) []int16 {
	out := make([]int16, FrameSamples)
	base := idx * FrameSamples
	for k := range out {
		v := 8000.0 * math.Sin(2*math.Pi*440*float64(base+k)/SampleRate)
		out[k] = int16(math.Round(v))
	}
	return out
}

// Generate builds the full arrival trace for cfg. The same seed and
// parameters always produce the same trace.
func Generate(cfg Config) []Event {
	rng := rand.New(rand.NewSource(cfg.Seed))
	events := make([]Event, 0, cfg.Frames)
	burstLeft := 0
	burstRun := 0
	for i := 0; i < cfg.Frames; i++ {
		if burstLeft > 0 {
			// Inside a burst episode: frame is lost, episode may extend.
			burstLeft--
			burstRun++
			if burstRun < 8 && rng.Float64() < 0.4 {
				burstLeft++
			}
			continue
		}
		if rng.Float64() < cfg.Loss {
			if rng.Float64() < cfg.Burst {
				burstLeft = 1 + rng.Intn(3)
				burstRun = 0
			}
			continue
		}
		arrival := int64(i*10) + jitter(rng, cfg.JitterMs)
		if rng.Float64() < cfg.Reorder {
			arrival += 5 + int64(rng.Intn(16))
		}
		data, err := packet.Encode(uint16(i), uint32(i*FrameSamples), packet.EncodePCM(PCMFrame(i)))
		if err != nil {
			panic(err) // cannot happen: fixed-size payload
		}
		events = append(events, Event{ArrivalMs: arrival, Data: data})
		if rng.Float64() < cfg.Duplicate {
			events = append(events, Event{ArrivalMs: arrival + 3, Data: data})
		}
	}
	sort.SliceStable(events, func(a, b int) bool {
		return events[a].ArrivalMs < events[b].ArrivalMs
	})
	return events
}

func jitter(rng *rand.Rand, ms float64) int64 {
	if ms <= 0 {
		return 0
	}
	return int64(math.Round((rng.Float64()*2 - 1) * ms))
}
