package main

import (
	"mediaforensics/internal/fixture"
	"mediaforensics/internal/rtp"
)

// runRTP builds and recovers the standard damaged transport stream.
func runRTP(seed int64, frames int, loss float64) rtp.Stats {
	cfg := fixture.DefaultRTPConfig(seed, frames*4, loss)
	stream := fixture.GenerateRTP(cfg)
	wire, _ := fixture.MarshalPackets(stream)
	rec := rtp.NewRecoverer(rtp.DefaultWindow, cfg.FrameStep, cfg.ClockRate)
	for _, raw := range wire {
		_ = rec.Ingest(raw)
	}
	stats, _ := rec.Close()
	return stats
}
