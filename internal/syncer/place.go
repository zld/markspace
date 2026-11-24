package syncer

import (
	"mediarun/internal/clock"
	"mediarun/internal/fault"
	"mediarun/internal/recovery"
)

// Place converts recovered units to local timeline ticks. The returned faults
// describe wrap ambiguity or non-monotonic timestamps; placement itself
// continues so downstream stages can still report structure.
func Place(units []recovery.Unit, tl *clock.Timeline, cfg Config) ([]Placed, fault.List) {
	var faults fault.List
	if tl == nil || len(units) == 0 {
		return nil, faults
	}
	if err := cfg.validate(); err != nil {
		faults = append(faults, fault.New(fault.StageSync, fault.KindConfig, "%v", err))
		return nil, faults
	}
	conv := &clock.Converter{
		Timeline:  tl,
		ClockRate: cfg.AudioClockRate,
		Offset:    cfg.BaseTick + cfg.AudioOffsetTicks,
	}
	if _, err := conv.Prime(cfg.BaseRTP); err != nil {
		e := fault.Wrap(fault.StageSync, fault.KindTimestamp, err, "prime audio converter")
		e.WithTrack(cfg.AudioTrack)
		faults = append(faults, e)
		return nil, faults
	}
	out := make([]Placed, 0, len(units))
	prev := int64(0)
	for i, u := range units {
		ts := u.RTPTimestamp
		if u.Packet != nil {
			ts = u.Packet.Timestamp
		}
		tick, err := conv.LocalTick(ts)
		if err != nil {
			e := fault.Wrap(fault.StageSync, fault.KindTimestamp, err,
				"audio slot %d sequence %d", i, u.Seq)
			e.WithTrack(cfg.AudioTrack)
			faults = append(faults, e)
			continue
		}
		if i > 0 && tick < prev {
			e := fault.New(fault.StageSync, fault.KindTimestamp,
				"audio slot %d tick %d precedes previous %d", i, tick, prev)
			e.WithTrack(cfg.AudioTrack)
			faults = append(faults, e)
		}
		prev = tick
		dur := clock.ScaleTicks(u.TickSpan, cfg.AudioClockRate, tl.Timescale)
		if dur <= 0 {
			dur = 1
		}
		out = append(out, Placed{Unit: u, Tick: tick, Duration: dur})
	}
	return out, faults
}
