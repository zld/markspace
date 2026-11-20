// Package receiver wires wire packets into the shared jitter controller through
// either a deterministic simulation or a real UDP socket.
package receiver

import (
	"time"

	"jitterlab/internal/jitter"
	"jitterlab/internal/trace"
)

// SimConfig controls deterministic playback of a generated trace.
type SimConfig struct {
	// Tick is the wall-clock granularity at which the simulated clock advances.
	Tick time.Duration
	// Step is the simulated time advanced per tick.
	Step time.Duration
}

// DefaultSimConfig uses 5 ms ticks so playout deadlines are resolved finely
// without generating a disproportionate number of events.
func DefaultSimConfig() SimConfig {
	return SimConfig{Tick: 5 * time.Millisecond, Step: 5 * time.Millisecond}
}

func (c SimConfig) normalized() SimConfig {
	if c.Tick <= 0 {
		c.Tick = 5 * time.Millisecond
	}
	if c.Step <= 0 {
		c.Step = c.Tick
	}
	return c
}

// SimResult describes what the simulation fed to the controller.
type SimResult struct {
	Fed   int
	Final time.Duration
}

// RunSimulated feeds every trace delivery to ctrl in arrival order, advancing
// the controller clock in fixed steps. The same trace and config therefore
// produce byte-identical metrics regardless of machine speed.
func RunSimulated(ctrl *jitter.Controller, tr trace.Trace, cfg SimConfig) SimResult {
	cfg = cfg.normalized()
	end := tr.FinalArrival() + tr.FrameMS*4 + 500*time.Millisecond
	if floor := time.Duration(tr.TotalFrames) * tr.FrameMS; end < floor {
		end = floor
	}
	i := 0
	now := time.Duration(0)
	fed := 0
	for {
		for i < len(tr.Packets) && tr.Packets[i].At <= now {
			ctrl.AddPacket(tr.Packets[i].Data, tr.Packets[i].At)
			fed++
			i++
		}
		if i == len(tr.Packets) {
			// All arrivals are in flight: play out the remaining buffered
			// packets and one final watermark sample.
			ctrl.CloseInput()
			ctrl.AdvanceTo(end)
			break
		}
		ctrl.AdvanceTo(now)
		now += cfg.Step
	}
	return SimResult{Fed: fed, Final: end}
}
