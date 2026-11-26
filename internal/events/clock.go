package events

import (
	"fmt"

	"medtrace/internal/core"
)

// Timeline maps a track's RTP timestamps onto canonical media time. RTP
// timestamps have an arbitrary origin, so the timeline is anchored by the
// first observation; later observations are placed by their unwrapped
// distance from that anchor. A 32-bit wrap is folded back in, and a value that
// still steps backwards opens a new anchor rather than producing a negative
// offset.
type Timeline struct {
	clockRate uint32
	period    int64
	anchored  bool
	anchorRaw int64
	anchorPos core.Nanos
	lastRaw   int64
	wraps     int
	backsteps int
}

// NewTimeline creates a timeline for one RTP clock rate.
func NewTimeline(clockRate uint32) *Timeline {
	return &Timeline{clockRate: clockRate, period: int64(1) << 32}
}

// Anchor pins the first raw timestamp to a canonical position.
func (t *Timeline) Anchor(raw int64, pos core.Nanos) {
	t.anchored = true
	t.anchorRaw, t.anchorPos, t.lastRaw = raw, pos, raw
}

// Map converts a raw RTP timestamp to canonical time, unwrapping 32-bit
// counter rollover. It reports whether a wrap or a backwards step was folded.
func (t *Timeline) Map(raw int64) (core.Nanos, bool, bool, error) {
	if !t.anchored {
		return 0, false, false, fmt.Errorf("timeline has no anchor")
	}
	if t.clockRate == 0 {
		return 0, false, false, fmt.Errorf("timeline has no clock rate")
	}
	wrap, back := false, false
	if raw < t.lastRaw {
		if t.lastRaw-raw > t.period/2 {
			t.wraps++
			t.anchorRaw -= t.period
			wrap = true
		} else {
			t.backsteps++
			t.lastRaw = raw
			back = true
			return t.anchorPos, wrap, back, nil
		}
	}
	t.lastRaw = raw
	ticks := (t.lastRaw - t.anchorRaw)
	n, err := core.FromTicks(ticks, t.clockRate)
	if err != nil {
		return 0, wrap, back, err
	}
	return t.anchorPos + n, wrap, back, nil
}

// Wraps and Backsteps report counters for diagnostics.
func (t *Timeline) Wraps() int     { return t.wraps }
func (t *Timeline) Backsteps() int { return t.backsteps }

// Anomalies reports the client events whose RTP timestamp wrapped or stepped
// backwards, in wall-clock order, so the run's traceable listing can include
// client-side discontinuities alongside media-side ones.
func Anomalies(log *Log) []core.Anomaly {
	var out []core.Anomaly
	byTrack := map[string]*Timeline{}
	for _, rec := range log.Records {
		if rec.RTP.ClockRate == 0 {
			continue
		}
		key := rec.Track.Key()
		tl := byTrack[key]
		if tl == nil {
			tl = NewTimeline(rec.RTP.ClockRate)
			tl.Anchor(rec.RTP.Raw, core.Nanos(rec.WallNanos))
			byTrack[key] = tl
			continue
		}
		pos, wrap, back, err := tl.Map(rec.RTP.Raw)
		if err != nil {
			continue
		}
		if !wrap && !back {
			continue
		}
		reason := "timestamp-wrap"
		if back {
			reason = "timestamp-backwards"
		}
		out = append(out, core.Anomaly{
			Category:     core.CatTimeJump,
			Reason:       reason,
			Pos:          core.Pos{File: log.File, Line: rec.Line, Byte: -1},
			Track:        rec.Track,
			Segment:      rec.Segment,
			ByteOffset:   -1,
			RawTimestamp: rec.RTP.Raw,
			Timescale:    rec.RTP.ClockRate,
			Canonical:    pos,
			Detail:       "client RTP timestamp discontinuity",
		})
	}
	return out
}
