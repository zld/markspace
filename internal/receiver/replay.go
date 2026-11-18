package receiver

import (
	"fmt"
	"sort"

	"jitterlab/internal/buffer"
	"jitterlab/internal/metrics"
	"jitterlab/internal/packet"
	"jitterlab/internal/report"
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
	limit := int64(len(arrivals)+1)*frameMs + int64(cfg.Buffer.MaxWaitMs) + 2*frameMs
	maxTicks := int(limit/frameMs) + cfg.Options.Frames + 8

	next := 0
	finished := false
	for tickIdx := 0; tickIdx < maxTicks; tickIdx++ {
		at := int64(tickIdx) * frameMs
		for next < len(arrivals) && arrivals[next].at <= at {
			a := arrivals[next]
			next++
			err := decodeEvent(a.ev)
			rec.ObserveDatagram(len(a.ev.Wire), err)
			if gerr := guard.Observe(err); gerr != nil {
				res.Err = gerr
				res.ExitCode = ExitDamaged
				finished = true
				break
			}
			if err != nil {
				continue
			}
			f, _ := packet.Decode(a.ev.Wire)
			ctl.Offer(f, a.at)
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

	res.Summary = rec.Summary(ctl.Snapshot())
	res.Summary.Seed = cfg.Options.Seed
	res.Summary.Frames = len(res.Frames)
	res.Summary.TargetMs = ctl.TargetMs()
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

func decodeEvent(ev testkit.Event) error {
	if len(ev.Wire) == 0 {
		return packet.ErrTruncated
	}
	_, err := packet.Decode(ev.Wire)
	return err
}

// WriteResult emits frames then the summary line.
func WriteResult(w *report.Writer, res ReplayResult) error {
	for _, f := range res.Frames {
		if err := w.WriteFrame(f); err != nil {
			return err
		}
	}
	return w.WriteSummary(res.Summary)
}

var _ = fmt.Sprintf
