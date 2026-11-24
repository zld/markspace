// Package syncer aligns the audio RTP path and the frame path on the shared
// clock.Timeline.
//
// Place converts RTP timestamps into absolute local timeline ticks. It handles
// 32 bit timestamp wrap and applies the per-track start offset and the stream's
// declared base timestamp, so audio that genuinely starts late stays late
// instead of being re-anchored to video. Align then compares video coverage and
// audio coverage, tolerates sustained frame-rate changes and marks fragments
// whose drift exceeds the configured tolerance.
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
	// BaseRTP is the declared stream origin timestamp from the sidecar.
	BaseRTP uint32
	// BaseTick is the local timeline tick that BaseRTP maps to.
	BaseTick int64
	// AudioTrack is written into faults for operator clarity.
	AudioTrack uint8
}

func (c Config) validate() error {
	if c.ToleranceTicks < 0 {
		return fmt.Errorf("tolerance must be non-negative")
	}
	if c.AudioClockRate == 0 || c.AudioClockRate > 1_000_000 {
		return fmt.Errorf("audio clock rate %d is outside 1..1000000", c.AudioClockRate)
	}
	return nil
}

// Placed is one audio unit anchored on the timeline.
type Placed struct {
	Unit     recovery.Unit
	Tick     int64 // absolute local timeline tick of the unit start
	Duration int64 // timeline ticks
}

// End returns the exclusive end tick.
func (p Placed) End() int64 { return p.Tick + p.Duration }

// VideoFrame is a parsed frame placed on the timeline.
type VideoFrame struct {
	Record        *frames.Record
	LocalTick     int64
	DurationTicks int64
	RateChanged   bool // cadence differs from the previous sustained run
}

// AudioSlot is a placed audio unit as seen by the report.
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

// Align compares video sample coverage with the placed audio slots. Video PTS
// values must already be in the timeline timescale (the ingest format stores
// 90 kHz ticks).
func Align(video []*frames.Record, audio []Placed, tl *clock.Timeline, cfg Config) (*Report, error) {
	if tl == nil {
		return nil, fault.New(fault.StageSync, fault.KindConfig, "nil timeline")
	}
	if err := cfg.validate(); err != nil {
		return nil, fault.New(fault.StageSync, fault.KindConfig, "%v", err)
	}
	rep := &Report{Synced: true}
	if len(video) == 0 {
		rep.Synced = false
		rep.Faults = append(rep.Faults,
			fault.New(fault.StageSync, fault.KindIncomplete, "no video frames to align"))
		return rep, nil
	}

	rep.VideoStart = tl.Local(int64(video[0].Meta.PTS))
	prevTick := rep.VideoStart
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
				// A sustained cadence that differs from the previous run marks
				// the frame-rate transition. Isolated jitter does not.
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
	if rep.VideoEnd <= last.LocalTick {
		rep.VideoEnd = last.LocalTick
	}

	rep.AudioStart, rep.AudioEnd = rep.VideoStart, rep.VideoStart
	for _, p := range audio {
		rep.Audio = append(rep.Audio, AudioSlot{
			Unit:          p.Unit,
			LocalTick:     p.Tick,
			DurationTicks: p.Duration,
		})
	}
	if len(audio) > 0 {
		rep.AudioStart = audio[0].Tick
		rep.AudioEnd = audio[0].Tick
		for _, p := range audio {
			if p.Tick < rep.AudioStart {
				rep.AudioStart = p.Tick
			}
			if p.End() > rep.AudioEnd {
				rep.AudioEnd = p.End()
			}
		}
		// Start drift is the uncovered gap before the first audio slot that
		// reaches the video start. Audio already playing at the video start
		// contributes no drift, even if it began earlier.
		rep.StartDelta = startGap(audio, rep.VideoStart)
		rep.EndDelta = rep.AudioEnd - rep.VideoEnd
		if !WithinTolerance(rep.StartDelta, 0, cfg.ToleranceTicks) ||
			!WithinTolerance(rep.EndDelta, 0, cfg.ToleranceTicks) {
			rep.Synced = false
			e := fault.New(fault.StageSync, fault.KindDesync,
				"audio/video drift start=%d end=%d tolerance=%d",
				rep.StartDelta, rep.EndDelta, cfg.ToleranceTicks)
			e.WithTrack(cfg.AudioTrack)
			e.Recoverable = true
			rep.Faults = append(rep.Faults, e)
		}
	}

	// A hole far wider than the nominal cadence means the fragment timeline is
	// unknown; keep the audio comparison for diagnosis but mark incomplete.
	maxDur := maxDuration(rep.Video)
	for i := 1; i < len(rep.Video); i++ {
		if rep.Video[i].LocalTick-rep.Video[i-1].LocalTick > 4*maxDur {
			rep.Incomplete = true
			e := fault.New(fault.StageSync, fault.KindIncomplete,
				"video timeline hole before frame %d", i)
			e.At(rep.Video[i].Record.Offset, i).WithTrack(rep.Video[i].Record.Meta.Track)
			rep.Faults = append(rep.Faults, e)
			break
		}
	}

	sort.SliceStable(rep.Faults, func(i, j int) bool {
		if rep.Faults[i].Offset != rep.Faults[j].Offset {
			return rep.Faults[i].Offset < rep.Faults[j].Offset
		}
		return rep.Faults[i].Kind < rep.Faults[j].Kind
	})
	return rep, nil
}

// startGap returns how far after videoStart the first covering audio slot
// begins, or zero when audio already covers videoStart.
func startGap(audio []Placed, videoStart int64) int64 {
	best := int64(0)
	for _, p := range audio {
		if p.Tick <= videoStart && p.End() > videoStart {
			return 0
		}
		if p.Tick > videoStart {
			d := p.Tick - videoStart
			if best == 0 || d < best {
				best = d
			}
		}
	}
	return best
}

// WithinTolerance reports whether |a-b| is at most tol ticks.
func WithinTolerance(a, b, tol int64) bool {
	d := a - b
	if d < 0 {
		d = -d
	}
	return d <= tol
}

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
