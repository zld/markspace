package receiver

import (
	"sort"

	"jitterlab/internal/buffer"
	"jitterlab/internal/metrics"
	"jitterlab/internal/packet"
	"jitterlab/internal/testkit"
)

// ReplayConfig controls an offline run.
type ReplayConfig struct {
	Buffer        buffer.Config
	Options       testkit.Options
	Decay         float64
	MaxInvalidRun int
}

// ReplayResult is the outcome of an offline run.
type ReplayResult struct {
	Summary  metrics.Summary
	Frames   []metrics.Frame
	ExitCode int
	Err      error
}

// Exit codes shared by both receive paths.
const (
	ExitOK      = 0
	ExitUsage   = 2
	ExitDamaged = 3
	ExitOutput  = 4
)

// Replay feeds a generated plan through the same state machine used by
// the live receiver. The playout clock advances in fixed frame steps and
// arrivals are applied before each tick, so identical parameters and seed
// produce identical frames and summary.
func Replay(cfg ReplayConfig) (ReplayResult, error) {
	if err := cfg.Options.Validate(); err != nil {
		return ReplayResult{ExitCode: ExitUsage}, err
	}
	if cfg.MaxInvalidRun <= 0 {
		cfg.MaxInvalidRun = DefaultMaxInvalidRun
	}
	ctl, err := buffer.New(cfg.Buffer, cfg.Decay)
	if err != nil {
		return ReplayResult{ExitCode: ExitUsage}, err
	}
	guard, err := NewGuard(cfg.MaxInvalidRun)
	if err != nil {
		return ReplayResult{ExitCode: ExitUsage}, err
	}
	rec := metrics.New(cfg.Buffer, cfg.Options.Seed)

	plan, err := testkit.Generate(cfg.Options)
	if err != nil {
		return ReplayResult{ExitCode: ExitUsage}, err
	}

	type arrival struct {
		at  int64
		idx int
		ev  testkit.Event
	}
	var arrivals []arrival
	for i, ev := range plan.Events {
		if ev.Dropped {
			continue
		}
		arrivals = append(arrivals, arrival{at: cfg.Options.StretchMs(ev.AtMs), idx: i, ev: ev})
	}
	sort.SliceStable(arrivals, func(a, b int) bool {
		if arrivals[a].at != arrivals[b].at {
			return arrivals[a].at < arrivals[b].at
		}
		return arrivals[a].idx < arrivals[b].idx
	})

	res := ReplayResult{ExitCode: ExitOK}
	frameMs := int64(cfg.Buffer.FrameMs)
	// The playout clock only needs to cover the last arrival plus enough
	// ticks to play out what the buffer can hold; the controller sheds
	// beyond max-wait, and a drained stream finishes without extra ticks.
	endAt := int64(0)
	if n := len(arrivals); n > 0 {
		endAt = arrivals[n-1].at
	}
	maxTicks := int(endAt/frameMs) + cfg.Buffer.MaxWaitMs/cfg.Buffer.FrameMs + 4

	next := 0
	finished := false
	for tickIdx := 0; tickIdx < maxTicks; tickIdx++ {
		at := int64(tickIdx) * frameMs
		for next < len(arrivals) && arrivals[next].at <= at {
			a := arrivals[next]
			next++
			f, err := decodeEvent(a.ev)
			rec.ObserveDatagram(len(a.ev.Wire), err)
			if gerr := guard.Observe(err); gerr != nil {
				res.Err = gerr
				res.ExitCode = ExitDamaged
				finished = true
				break
			}
			if err == nil {
				ctl.Offer(f, a.at)
			}
		}
		if finished {
			break
		}
		if next == len(arrivals) {
			ctl.Finish()
		}
		slot := ctl.Tick(at)
		if slot.Played {
			res.Frames = append(res.Frames, rec.ObserveSlot(slot))
		}
		if slot.Finished {
			finished = true
			break
		}
	}

	rec.SetSenderStats(plan.Stats.Lost, plan.Stats.Duplicated, plan.Stats.Reordered, plan.Stats.Faults)
	res.Summary = rec.Summary(ctl.Snapshot())
	res.Summary.Seed = cfg.Options.Seed
	res.Summary.Frames = len(res.Frames)
	res.Summary.TargetMs = ctl.TargetMs()
	if res.Err != nil {
		res.ExitCode = ExitDamaged
	}
	return res, res.Err
}

// decodeEvent decodes one planned datagram, treating an empty wire payload
// as truncation so injected faults all flow through the same classifier.
func decodeEvent(ev testkit.Event) (packet.Frame, error) {
	if len(ev.Wire) == 0 {
		return packet.Frame{}, packet.ErrTruncated
	}
	return packet.Decode(ev.Wire)
}
