package timeline

import "fmt"

// buildTrack orders one track's blocks, recovers rollovers and fills gaps.
func buildTrack(spec TrackInput, raw []BlockInput, cfg MergeConfig) (*TrackTimeline, bool, string, error) {
	tl := &TrackTimeline{TrackNumber: spec.Number, UID: spec.UID, IsVideo: spec.IsVideo}
	tickRate := spec.SampleRate
	if spec.IsVideo {
		tickRate = spec.FrameRate
	}
	if tickRate <= 0 {
		return nil, false, "", New(CatConfig,
			"cannot derive tick rate for track").WithTrack(spec.UID)
	}
	tl.Rate = tickRate

	periodTicks := cfg.RolloverTicks
	wrapCount := 0
	wrapOffset := int64(0) // accumulated rollover periods in ticks
	var prevRaw int64
	havePrev := false
	var prevAbs, nominalStep int64

	for _, b := range raw {
		rawTicks := ticksFromNs(b.TimeNs, tickRate)
		offset, wraps, ok2 := recoverWrap(rawTicks, prevRaw, periodTicks, cfg.MaxStepTicks, havePrev, wrapCount, wrapOffset)
		if !ok2 {
			tl.Events = append(tl.Events, eventFromBlock(b, wrapCount, false))
			return tl, false, fmt.Sprintf(
				"ambiguous timecode rollover on track %d at offset %d", spec.Number, b.Offset), nil
		}
		wrapCount, wrapOffset = wraps, offset
		absTicks := rawTicks + wrapOffset
		absNs, ok3 := NsFromUnits(absTicks, tickRate)
		if !ok3 {
			return nil, false, "", New(CatOverflow,
				"recovered timestamp overflow").WithOffset(b.Offset).WithTrack(spec.UID)
		}
		ev := eventFromBlock(b, wrapCount, false)
		ev.TimeNs = absNs
		tl.Events = append(tl.Events, ev)

		if havePrev {
			step := absTicks - prevAbs
			if nominalStep == 0 && step > 0 {
				nominalStep = step // learn the per-block cadence
			}
			if gap := detectGap(spec, b, prevAbs, absTicks, tickRate, nominalStep); gap != nil {
				tl.Gaps = append(tl.Gaps, *gap)
				tl.Events = append(tl.Events, missingEvents(spec, *gap, b.Offset, nominalStep)...)
			}
		}
		prevRaw = rawTicks
		prevAbs = absTicks
		havePrev = true
	}
	if len(tl.Gaps) > cfg.MaxMissingIntervals {
		return tl, false, fmt.Sprintf("track %d exceeds missing-interval bound", spec.Number), nil
	}
	return tl, true, "", nil
}

// recoverWrap tracks the accumulated period offset. A backward RAW jump is
// explained by exactly one rollover only when the recovered absolute step is
// within the configured MaxStepTicks; a jump that needs more than one period,
// or one whose recovered step is implausibly large, is rejected as ambiguous
// rather than guessed.
func recoverWrap(raw, prevRaw, period, maxStep int64, havePrev bool, wraps int, offset int64) (int64, int, bool) {
	if !havePrev || period <= 0 || raw >= prevRaw {
		return offset, wraps, true
	}
	if prevRaw-raw >= period {
		return offset, wraps, false // more than one period could fit
	}
	newOffset, ok := addCheck(offset, period)
	if !ok {
		return offset, wraps, false
	}
	recoveredStep := (raw + newOffset) - (prevRaw + offset)
	if maxStep > 0 && recoveredStep > maxStep {
		return offset, wraps, false
	}
	return newOffset, wraps + 1, true
}

func detectGap(spec TrackInput, cur BlockInput, prevTicks, curTicks, rate, nominalStep int64) *Gap {
	if nominalStep <= 0 {
		nominalStep = 1
	}
	delta := curTicks - prevTicks
	// Rounding between the nanosecond clock and the track tick rate can move
	// a normal step by a small fraction, so a gap requires the step to exceed
	// roughly one-and-a-half nominal block cadences. A genuine single missing
	// block doubles the cadence and still trips this threshold.
	if delta*2 <= nominalStep*3 {
		return nil
	}
	startNs, ok1 := NsFromUnits(prevTicks+nominalStep, rate)
	endNs, ok2 := NsFromUnits(curTicks, rate)
	if !ok1 || !ok2 {
		return nil
	}
	missingUnits := (delta - 1) / nominalStep
	return &Gap{
		TrackNumber: spec.Number,
		StartNs:     startNs,
		EndNs:       endNs,
		Offset:      cur.Offset,
		Reason:      fmt.Sprintf("%d missing blocks on track %d", missingUnits, spec.Number),
	}
}

func missingEvents(spec TrackInput, g Gap, offset, nominalStep int64) []*Event {
	rate := spec.SampleRate
	if spec.IsVideo {
		rate = spec.FrameRate
	}
	if rate <= 0 || nominalStep <= 0 {
		return nil
	}
	step, ok := NsFromUnits(nominalStep, rate)
	if !ok {
		return nil
	}
	var out []*Event
	for t := g.StartNs; t < g.EndNs; {
		out = append(out, &Event{
			TrackNumber: spec.Number, TimeNs: t, Offset: offset,
			Missing: true, CheckOK: false,
		})
		t += step
	}
	return out
}

func ticksFromNs(ns, rate int64) int64 {
	ticks, ok := UnitsFromNs(ns, rate)
	if !ok {
		return 0
	}
	return ticks
}

func eventFromBlock(b BlockInput, wraps int, missing bool) *Event {
	return &Event{
		TrackNumber: b.TrackNumber,
		TimeNs:      b.TimeNs,
		Offset:      b.Offset,
		Size:        b.Size,
		CheckOK:     b.CheckOK,
		Missing:     missing,
		WrapCount:   wraps,
	}
}

func addCheck(a, b int64) (int64, bool) {
	sum := a + b
	if (a^b) >= 0 && (sum^a) < 0 {
		return 0, false
	}
	return sum, true
}
