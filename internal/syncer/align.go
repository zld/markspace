// Package syncer aligns the audio RTP path and the frame path on the shared
// clock.Timeline. It converts extended RTP timestamps to local media ticks,
// applies explicit per-track start offsets, tolerates nominal frame-rate
// changes and flags fragments whose audio/video drift exceeds the configured
// tolerance.
package syncer

import (
	"fmt"
	"sort"

	"mediarun/internal/clock"
	"mediarun/internal/fault"
	"mediarun/internal/frames"
	"mediarun/internal/recovery"
)

// Config describes one alignment run.
type Config struct {
	// ToleranceTicks is the allowed audio/video drift in timeline ticks.
	ToleranceTicks int64
	// AudioClockRate is the RTP clock rate of the audio track (e.g. 48000).
	AudioClockRate uint32
	// AudioOffsetTicks is the intended audio start relative to video start.
	// Positive values delay audio; negative advances it.
	AudioOffsetTicks int64
	// AudioTrack is written into faults for operator clarity.
	AudioTrack uint8
}

func (c Config) validate() error {
	if c.ToleranceTicks < 0 {
		return fmt.Errorf("tolerance must be non-negative")
	}
	if c.AudioClockRate == 0 {
		return fmt.Errorf("audio clock rate must be non-zero")
	}
	if c.AudioClockRate > 1_000_000 {
		return fmt.Errorf("audio clock rate %d is implausible", c.AudioClockRate)
	}
	return nil
}

// VideoFrame is a parsed frame placed on the timeline.
type VideoFrame struct {
	Record        *frames.Record
	LocalTick     int64
	DurationTicks int64
	RateChanged   bool // PTS delta differs from the previous frame
}

// AudioSlot is a recovered audio unit placed on the timeline.
type AudioSlot struct {
	Unit          recovery.Unit
	LocalTick     int64
	DurationTicks int64
}

// Report is the alignment result for one fragment.
type Report struct {
	Video       []VideoFrame
	Audio       []AudioSlot
	VideoStart  int64
	VideoEnd    int64
	AudioStart  int64
	AudioEnd    int64
	StartDelta  int64
	EndDelta    int64
	Synced      bool
	RateChanges int
	Incomplete  bool
	Faults      fault.List
}

// Align places frames and recovered audio slots on the timeline. Both inputs
// must already be validated; the function is pure with respect to its inputs
// so repeated runs produce identical reports.
func Align(video []*frames.Record, audio []recovery.Unit, tl *clock.Timeline, cfg Config) (*Report, error) {
	if tl == nil {
		return nil, fault.New(fault.StageSync, fault.KindConfig, "nil timeline")
	}
	if err := cfg.validate(); err != nil {
		return nil, fault.New(fault.StageSync, fault.KindConfig, "%v", err)
	}
	rep := &Report{Synced: true}
	if len(video) == 0 {
		rep.Synced = false
		e := fault.New(fault.StageSync, fault.KindIncomplete, "no video frames to align")
		rep.Faults = append(rep.Faults, e)
		return rep, nil
	}

	// Video timestamps are already in the timeline timescale. Origin is the
	// earliest presentation time so track start offsets are preserved as a
	// relative shift rather than being discarded.
	rep.VideoStart = tl.Local(int64(video[0].Meta.PTS))
	prevTick := rep.VideoStart
	// Rate changes are detected from sustained cadence runs: a new delta that
	// follows at least three identical deltas marks the first frame of the new
	// cadence. This tolerates a single jittered timestamp while still noticing
	// a 24/25 or 25/30 fps switch.
	prevDelta := int64(0)
	runLen := 0
	for i, rec := range video {
		tick := tl.Local(int64(rec.Meta.PTS))
		if i > 0 && tick < prevTick {
			e := fault.New(fault.StageSync, fault.KindTimestamp,
				"frame %d pts %d precedes previous %d", i, rec.Meta.PTS, video[i-1].Meta.PTS)
			e.At(rec.Offset, i).WithTrack(rec.Meta.Track)
			rep.Faults = append(rep.Faults, e)
			rep.Synced = false
		}
		vf := VideoFrame{Record: rec, LocalTick: tick}
		if i > 0 {
			vf.DurationTicks = tick - prevTick
			if vf.DurationTicks < 0 {
				vf.DurationTicks = 0
			}
			switch {
			case i == 1:
				prevDelta, runLen = vf.DurationTicks, 1
			case vf.DurationTicks == prevDelta:
				runLen++
			default:
				if runLen >= 3 {
					vf.RateChanged = true
					rep.RateChanges++
				}
				prevDelta, runLen = vf.DurationTicks, 1
			}
		}
		rep.Video = append(rep.Video, vf)
		prevTick = tick
	}
	last := rep.Video[len(rep.Video)-1]
	rep.VideoEnd = last.LocalTick + last.DurationTicks
	if last.DurationTicks == 0 && len(rep.Video) > 1 {
		rep.VideoEnd = last.LocalTick
	}

	rep.AudioStart, rep.AudioEnd = rep.VideoStart, rep.VideoStart
	if len(audio) > 0 {
		conv := &clock.Converter{
			Timeline:  tl,
			ClockRate: cfg.AudioClockRate,
			Offset:    rep.VideoStart + cfg.AudioOffsetTicks,
		}
		prev := int64(0)
		for i, unit := range audio {
			var ts uint32
			if unit.Packet != nil {
				ts = unit.Packet.Timestamp
			} else {
				ts = unit.RTPTimestamp
			}
			tick, err := conv.LocalTick(ts)
			if err != nil {
				e := fault.Wrap(fault.StageSync, fault.KindTimestamp, err,
					"audio slot %d sequence %d", i, unit.Seq)
				e.WithTrack(cfg.AudioTrack)
				rep.Faults = append(rep.Faults, e)
				rep.Synced = false
				continue
			}
			slot := AudioSlot{
				Unit:          unit,
				LocalTick:     tick,
				DurationTicks: clock.ScaleTicks(unit.TickSpan, cfg.AudioClockRate, tl.Timescale),
			}
			if i > 0 && tick < prev {
				e := fault.New(fault.StageSync, fault.KindTimestamp,
					"audio slot %d tick %d precedes previous %d", i, tick, prev)
				e.WithTrack(cfg.AudioTrack)
				rep.Faults = append(rep.Faults, e)
				rep.Synced = false
			}
			prev = tick
			rep.Audio = append(rep.Audio, slot)
		}
		if len(rep.Audio) > 0 {
			rep.AudioStart = rep.Audio[0].LocalTick
			rep.AudioEnd = rep.AudioStart
			for _, slot := range rep.Audio {
				rep.AudioEnd += slot.DurationTicks
			}
		}
	}

	// A gap in the video path means the fragment timeline is unknown; mark it
	// incomplete but keep the audio alignment for diagnosis.
	for i := 1; i < len(rep.Video); i++ {
		if rep.Video[i].LocalTick-rep.Video[i-1].LocalTick > 4*maxDuration(rep.Video) {
			rep.Incomplete = true
			e := fault.New(fault.StageSync, fault.KindIncomplete,
				"video timeline hole before frame %d", i)
			e.At(rep.Video[i].Record.Offset, i).WithTrack(rep.Video[i].Record.Meta.Track)
			rep.Faults = append(rep.Faults, e)
			break
		}
	}

	rep.StartDelta = rep.AudioStart - (rep.VideoStart + cfg.AudioOffsetTicks)
	rep.EndDelta = rep.AudioEnd - (rep.VideoEnd + cfg.AudioOffsetTicks)
	if abs64(rep.StartDelta) > cfg.ToleranceTicks || abs64(rep.EndDelta) > cfg.ToleranceTicks {
		rep.Synced = false
		if len(rep.Faults) == 0 || !hasDesync(rep.Faults) {
			e := fault.New(fault.StageSync, fault.KindDesync,
				"audio/video drift start=%d end=%d tolerance=%d",
				rep.StartDelta, rep.EndDelta, cfg.ToleranceTicks)
			e.WithTrack(cfg.AudioTrack)
			e.Recoverable = true
			rep.Faults = append(rep.Faults, e)
		}
	}
	sort.SliceStable(rep.Faults, func(i, j int) bool {
		return rep.Faults[i].Offset < rep.Faults[j].Offset
	})
	return rep, nil
}

func hasDesync(l fault.List) bool { return l.CountKind(fault.KindDesync) > 0 }

func maxDuration(v []VideoFrame) int64 {
	var m int64
	for _, f := range v {
		if f.DurationTicks > m {
			m = f.DurationTicks
		}
	}
	if m == 0 {
		m = 1
	}
	return m
}

func abs64(v int64) int64 {
	if v < 0 {
		return -v
	}
	return v
}
