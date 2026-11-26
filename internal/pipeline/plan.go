package pipeline

import (
	"fmt"
	"math"

	"replay/internal/clip"
	"replay/internal/clock"
	"replay/internal/events"
	"replay/internal/media"
)

// planEvents turns the ordered feed into per-event verdicts. The rules
// follow the acceptance order: RTP validity, confidence, duplicate,
// anomaly trust, keyframe availability, duration budget.
func planEvents(c *Config, tl *media.Timeline, feed *events.Feed, st *Stats) []*plannedEvent {
	v := tl.Video
	preTick := durationToTicks(c.Pre, v.Timescale)
	postTick := durationToTicks(c.Post, v.Timescale)
	budgetTick := durationToTicks(c.Budget, v.Timescale)

	out := make([]*plannedEvent, 0, len(feed.Events))
	for _, ev := range feed.Events {
		pe := &plannedEvent{ev: ev, decision: events.DecisionAccepted, videoTS: v.Timescale}
		switch ev.Decision {
		case events.DecisionRTPBad:
			pe.decision = events.DecisionRTPBad
			pe.why = ev.RejectWhy
			out = append(out, pe)
			continue
		case events.DecisionSkipConfidence:
			pe.decision = events.DecisionSkipConfidence
			pe.why = ev.RejectWhy
			st.SkipConfidence++
			out = append(out, pe)
			continue
		case events.DecisionDuplicate:
			pe.decision = events.DecisionDuplicate
			pe.why = "identical event already seen"
			st.Duplicate++
			out = append(out, pe)
			continue
		}

		// Resample the event time onto the video track's native clock.
		evMT := ev.MediaTime
		videoTime, err := evMT.ToScale(v.Timescale)
		if err != nil {
			pe.decision = events.DecisionRTPBad
			pe.why = err.Error()
			out = append(out, pe)
			continue
		}
		reqStart := videoTime.Ticks - preTick
		reqEnd := videoTime.Ticks + eventDurationTicks(ev, v.Timescale) + postTick
		pe.spec = clip.ClipSpec{
			EventID: ev.ID, EventStart: reqStart, WindowEnd: reqEnd,
		}

		// Anomaly trust: any window touching a track anomaly is rejected.
		if a := v.AffectedByAnomaly(reqStart, reqEnd); a != nil {
			pe.decision = events.DecisionSkipUntrusted
			pe.anomaly = a
			pe.why = fmt.Sprintf("window intersects %s anomaly on track %d", a.Kind, a.TrackID)
			st.SkipUntrusted++
			st.Affected++
			out = append(out, pe)
			continue
		}
		if tl.Audio != nil {
			if a := audioAnomaly(tl, v, reqStart, reqEnd); a != nil {
				pe.decision = events.DecisionSkipUntrusted
				pe.anomaly = a
				pe.why = "window intersects audio anomaly"
				st.SkipUntrusted++
				st.Affected++
				out = append(out, pe)
				continue
			}
		}

		// Keyframe alignment: snap start back to the previous sync sample.
		syncIdx := v.SyncAtOrBefore(reqStart)
		if syncIdx < 0 {
			pe.decision = events.DecisionNoKeyframe
			pe.why = "no sync sample at or before the requested start"
			st.NoKeyframe++
			out = append(out, pe)
			continue
		}
		syncPTS := v.Samples[syncIdx].PTS
		pe.spec.SyncStart = syncPTS

		// The actual clip length is dictated by the keyframe and the
		// half-open window [syncPTS, reqEnd): the first sample at or past
		// reqEnd is excluded. The budget is checked on that real length.
		endIdx := v.FirstSampleAtOrAfter(reqEnd) - 1
		if endIdx >= len(v.Samples) {
			endIdx = len(v.Samples) - 1
		}
		if endIdx < syncIdx {
			endIdx = syncIdx
		}
		realEnd := v.Samples[endIdx].PTS + int64(v.Samples[endIdx].Dur)
		pe.clipDur = realEnd - syncPTS
		pe.deviation = ticksToSeconds(pe.clipDur, v.Timescale) - c.Budget.Seconds()
		if pe.clipDur > budgetTick {
			pe.decision = events.DecisionSkipBudget
			pe.why = fmt.Sprintf("aligned length %d ticks exceeds budget %d", pe.clipDur, budgetTick)
			st.SkipBudget++
			out = append(out, pe)
			continue
		}
		out = append(out, pe)
	}
	return out
}

// audioAnomaly maps the video window onto the audio clock and checks it.
func audioAnomaly(tl *media.Timeline, v *media.TrackTimeline, vStart, vEnd int64) *media.AnomalyInterval {
	a := tl.Audio
	s, err := mediaTimeAt(vStart, v.Timescale, a.Timescale)
	if err != nil {
		return nil
	}
	e, err := mediaTimeAt(vEnd, v.Timescale, a.Timescale)
	if err != nil {
		return nil
	}
	return a.AffectedByAnomaly(s, e)
}

func mediaTimeAt(tick int64, fromScale, toScale uint32) (int64, error) {
	mt, err := clock.NewMediaTime(tick, fromScale)
	if err != nil {
		return 0, err
	}
	out, err := mt.ToScale(toScale)
	if err != nil {
		return 0, err
	}
	return out.Ticks, nil
}

func durationToTicks(d interface{ Nanoseconds() int64 }, timescale uint32) int64 {
	// Exact conversion: ns/1e9 * timescale, rounded to nearest tick.
	num := d.Nanoseconds() * int64(timescale)
	q := num / 1_000_000_000
	rem := num % 1_000_000_000
	if abs64(rem)*2 >= 1_000_000_000 {
		if rem > 0 {
			q++
		} else {
			q--
		}
	}
	return q
}

func eventDurationTicks(ev *events.AlignedEvent, timescale uint32) int64 {
	if ev.DurationS <= 0 {
		return 0
	}
	return int64(math.Round(ev.DurationS * float64(timescale)))
}

func ticksToSeconds(ticks int64, timescale uint32) float64 {
	return float64(ticks) / float64(timescale)
}

func abs64(v int64) int64 {
	if v < 0 {
		return -v
	}
	return v
}

func (pe *plannedEvent) timescale() uint32 { return pe.videoTS }
