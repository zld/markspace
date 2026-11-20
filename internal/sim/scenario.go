// Package sim deterministically generates impaired packet schedules
// (delay, reorder, duplicates, random and burst loss) from a seed.
package sim

import (
	"math"
	"math/rand"
	"sort"
	"time"

	"jitterlab/internal/plc"
	"jitterlab/internal/wire"
)

// Event is one scheduled transmission of one packet.
type Event struct {
	At  time.Duration // send time relative to run start
	Pkt wire.Packet
}

// Config describes the impairment scenario.
type Config struct {
	Seed        int64
	Duration    time.Duration
	FramePeriod time.Duration
}

// tonePCM renders one 20 ms frame of a 450 Hz tone (exactly 9 cycles
// per frame, so consecutive frames are boundary-continuous).
func tonePCM(phase float64) ([]byte, float64) {
	s := make([]int16, plc.FrameSamples)
	for i := range s {
		v := 9000 * math.Sin(phase)
		s[i] = int16(v)
		phase += 2 * math.Pi * 450 / plc.SampleRate
	}
	// Wrap phase to keep float precision over long runs.
	phase = math.Mod(phase, 2*math.Pi)
	return plc.PCMFromSamples(s), phase
}

// Generate builds the send schedule. The sequence space always wraps at
// least once: numbering starts close to 65535.
func Generate(cfg Config) []Event {
	rng := rand.New(rand.NewSource(cfg.Seed))
	total := int(cfg.Duration / cfg.FramePeriod)
	if total < 1 {
		total = 1
	}
	// Start near the wrap point so seq crosses 65535->0 during the run.
	backoff := total / 4
	if backoff < 1 {
		backoff = 1
	}
	if backoff > 1000 {
		backoff = 1000
	}
	startSeq := uint16(65536 - backoff)

	// Burst-loss windows: two episodes of 5-12 consecutive drops.
	burstLost := make(map[int]bool)
	for b := 0; b < 2; b++ {
		start := total/3 + rng.Intn(total/2+1)
		length := 5 + rng.Intn(8)
		for i := start; i < start+length && i < total; i++ {
			burstLost[i] = true
		}
	}
	// One jitter-burst window: arrivals bunch up and spread out.
	jbStart := total / 5
	jbEnd := jbStart + total/5

	var events []Event
	phase := 0.0
	for i := 0; i < total; i++ {
		pcm, ph := tonePCM(phase)
		phase = ph
		pkt := wire.Packet{
			Seq:       startSeq + uint16(i),
			Timestamp: uint32(i) * uint32(cfg.FramePeriod/time.Millisecond),
			Payload:   pcm,
		}
		if burstLost[i] {
			continue
		}
		if rng.Float64() < 0.02 { // random isolated loss
			continue
		}
		at := time.Duration(i) * cfg.FramePeriod
		// Baseline jitter +-10% of a period.
		at += time.Duration((rng.Float64() - 0.5) * 0.2 * float64(cfg.FramePeriod))
		if i >= jbStart && i < jbEnd {
			// Jitter burst: swings up to +-3 periods.
			at += time.Duration((rng.Float64() - 0.5) * 6 * float64(cfg.FramePeriod))
		}
		if rng.Float64() < 0.08 { // reorder: extra 1-3 period delay
			at += time.Duration(1+rng.Intn(3)) * cfg.FramePeriod
		}
		if at < 0 {
			at = 0
		}
		events = append(events, Event{At: at, Pkt: pkt})
		if rng.Float64() < 0.03 { // duplicate delivery
			dup := Event{At: at + cfg.FramePeriod/4, Pkt: pkt}
			events = append(events, dup)
		}
	}
	sort.Slice(events, func(a, b int) bool { return events[a].At < events[b].At })
	return events
}
