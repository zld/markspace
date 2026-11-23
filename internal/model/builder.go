package model

import (
	"mediaforensics/internal/clock"
)

// Build normalizes every track into a unified timeline measured in exact
// rational seconds. It never uses floating point. A track that hits an
// arithmetic overflow is blocked (with its first source offset preserved)
// while the remaining tracks still produce a usable timeline.
func Build(tracks []*Track) *Timeline {
	tl := &Timeline{Duration: clock.Zero}
	for _, tr := range tracks {
		tl.Tracks = append(tl.Tracks, tr)
		switch tr.Kind {
		case KindVideo:
			tl.VideoTracks = append(tl.VideoTracks, tr.ID)
		case KindAudio:
			tl.AudioTracks = append(tl.AudioTracks, tr.ID)
		}
		if err := normalizeTrack(tr); err != nil {
			tr.Blocked = true
			tr.BlockReason = err.Error()
			tl.AddError(err)
			continue
		}
		if end, err := trackEnd(tr); err == nil {
			if c, _ := end.Cmp(tl.Duration); c > 0 {
				tl.Duration = end
			}
		} else {
			tr.Blocked = true
			tr.BlockReason = err.Error()
			tl.AddError(err)
		}
	}
	return tl
}

// normalizeTrack converts raw timescale ticks into seconds and applies the
// clip schedule, including non-zero composition offsets and zero-length clips.
func normalizeTrack(tr *Track) *ForensicError {
	base := clock.New(1, tr.Timescale)
	for i := range tr.Samples {
		s := &tr.Samples[i]
		start, err := base.Mul(clock.New(s.Start.Num(), 1))
		if err != nil {
			return overflowError(tr, s.Offset, "sample start conversion")
		}
		dur, err := base.Mul(clock.New(s.Duration.Num(), 1))
		if err != nil {
			return overflowError(tr, s.Offset, "sample duration conversion")
		}
		s.Start, s.Duration = start, dur
	}
	for ci := range tr.Clips {
		c := &tr.Clips[ci]
		ts, err := base.Mul(clock.New(c.TrackStart.Num(), 1))
		if err != nil {
			return overflowError(tr, tr.EarliestOffset, "clip track start conversion")
		}
		to, err := base.Mul(clock.New(c.TrackOffset.Num(), 1))
		if err != nil {
			return overflowError(tr, tr.EarliestOffset, "clip offset conversion")
		}
		c.TrackStart, c.TrackOffset = ts, to
		if c.ToSample <= c.FromSample {
			c.ZeroLength = true
		}
	}
	return nil
}

// Schedule flattens a track's clips into absolute timeline placements.
// Overlapping clips are all retained (the renderer defines precedence);
// zero-length clips produce no placements. Overflow blocks the track.
func Schedule(tr *Track) ([]ScheduledSample, *ForensicError) {
	if tr.Blocked {
		return nil, NewError(StageClock, "schedule_blocked", tr.ID,
			tr.EarliestOffset, tr.BlockReason)
	}
	var out []ScheduledSample
	for _, c := range tr.Clips {
		if c.ZeroLength {
			continue
		}
		if c.FromSample < 0 || c.ToSample > len(tr.Samples) || c.FromSample >= c.ToSample {
			return nil, NewError(StageClock, "clip_bounds", tr.ID,
				tr.EarliestOffset, "clip references samples outside the track")
		}
		running := c.TrackStart
		for i := c.FromSample; i < c.ToSample; i++ {
			s := tr.Samples[i]
			at, err := running.Add(c.TrackOffset)
			if err != nil {
				return nil, overflowError(tr, s.Offset, "clip composition overflow")
			}
			out = append(out, ScheduledSample{Sample: s, At: at, Clip: c})
			next, err := running.Add(s.Duration)
			if err != nil {
				return nil, overflowError(tr, s.Offset, "clip accumulation overflow")
			}
			running = next
		}
	}
	return out, nil
}

func trackEnd(tr *Track) (clock.Rational, *ForensicError) {
	end := clock.Zero
	for _, c := range tr.Clips {
		if c.ZeroLength {
			continue
		}
		if c.FromSample < 0 || c.ToSample > len(tr.Samples) {
			return clock.Zero, NewError(StageClock, "clip_bounds", tr.ID,
				tr.EarliestOffset, "clip references samples outside the track")
		}
		last := tr.Samples[c.ToSample-1]
		// Clip end = clip start + sum of covered sample durations.
		dur := clock.Zero
		for i := c.FromSample; i < c.ToSample; i++ {
			d, err := dur.Add(tr.Samples[i].Duration)
			if err != nil {
				return clock.Zero, overflowError(tr, tr.Samples[i].Offset, "duration sum overflow")
			}
			dur = d
		}
		ce, err := c.TrackStart.Add(dur)
		if err != nil {
			return clock.Zero, overflowError(tr, last.Offset, "clip end overflow")
		}
		if cmp, _ := ce.Cmp(end); cmp > 0 {
			end = ce
		}
	}
	return end, nil
}

func overflowError(tr *Track, offset int64, phase string) *ForensicError {
	return NewError(StageClock, phase, tr.ID, offset, clock.ErrOverflow.Error())
}
