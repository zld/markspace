// Package timeline repairs fragment timestamps. Gaps in mfhd sequence
// numbers are reported but never filled with synthetic samples; a tfdt that
// regresses is relocated to the previous fragment's end plus its own sample
// durations, and every change is recorded with original and repaired values.
package timeline

import (
	"fmt"

	"mediaroute/internal/model"
)

// Stage identifies errors emitted by this subsystem.
const Stage = "timeline/repair"

// Result bundles the repaired source set with the audit records needed by the
// report and delivery stages.
type Result struct {
	Set          *model.SourceSet
	Repairs      []model.RepairRecord
	MFHDGaps     int
	Duplicates   int
	RepairedTfdt int
	LastSequence map[uint32]uint32
}

// trackClock tracks the monotonic decode clock for one track.
type trackClock struct {
	initialised bool
	hasEnd      bool
	lastStart   uint64 // start of the most recent fragment, in ticks
	lastEnd     uint64 // exclusive end of the most recently written samples
	lastSeq     uint32
	seenSeq     bool
	timescale   uint32
}

// Repairer applies timeline repair incrementally, one fragment at a time, so
// it can run as a pipeline stage. It holds only per-track clock state and the
// accumulating audit records, never the full fragment set.
type Repairer struct {
	clocks   map[uint32]*trackClock
	tsByID   map[uint32]uint32
	res      *Result
	seqClock seqClock
}

// seqClock tracks the mfhd sequence stream, which belongs to the moof rather
// than to any single track.
type seqClock struct {
	seen bool
	last uint32
}

// NewRepairer validates the init metadata and prepares per-track clocks.
func NewRepairer(set *model.SourceSet) (*Repairer, error) {
	if set == nil {
		return nil, model.NewTaskError(model.CategoryInvalidConfig, Stage, "nil source set")
	}
	if err := set.Validate(); err != nil {
		return nil, err
	}
	r := &Repairer{
		clocks: make(map[uint32]*trackClock, len(set.Tracks)),
		tsByID: make(map[uint32]uint32, len(set.Tracks)),
		res:    &Result{Set: set, LastSequence: make(map[uint32]uint32, len(set.Tracks))},
	}
	for _, t := range set.Tracks {
		r.tsByID[t.TrackID] = t.Timescale
	}
	return r, nil
}

// Result returns the accumulated repair records.
func (r *Repairer) Result() *Result { return r.res }

// Apply repairs one fragment in source order. Discarded fragments are skipped
// entirely: they must not advance any track clock.
func (r *Repairer) Apply(frag *model.Fragment) error {
	if frag.Discarded || frag.Truncated {
		return nil
	}
	if err := r.accountSequence(frag); err != nil {
		return err
	}
	for ti := range frag.Tracks {
		ft := &frag.Tracks[ti]
		ts := r.tsByID[ft.TrackID]
		if ts == 0 {
			return model.NewTaskError(model.CategoryZeroTimescale, Stage,
				fmt.Sprintf("track %d has no timescale in init", ft.TrackID)).WithTrack(ft.TrackID)
		}
		ft.Timescale = ts
		clk := r.clocks[ft.TrackID]
		if clk == nil {
			clk = &trackClock{timescale: ts}
			r.clocks[ft.TrackID] = clk
		}
		if err := repairTrack(frag, ft, clk, r.res); err != nil {
			return err
		}
	}
	return nil
}

// accountSequence performs mfhd accounting once per moof. The mfhd sequence
// number belongs to the moof, not to each traf, so a gap is counted once.
func (r *Repairer) accountSequence(frag *model.Fragment) error {
	if len(frag.Tracks) == 0 {
		return nil
	}
	seqKnown := false
	for i := range frag.Tracks {
		if frag.Tracks[i].SequenceKnown {
			frag.Sequence = frag.Tracks[i].Sequence
			seqKnown = true
			break
		}
	}
	if !seqKnown {
		return nil
	}
	clk := &r.seqClock
	if clk.seen {
		switch {
		case frag.Sequence == clk.last:
			frag.Duplicate = true
			r.res.Duplicates++
			frag.AddIssue(model.Issue{
				Category: model.CategoryDuplicateSequence,
				Stage:    Stage,
				Offset:   frag.MoofRange.Start,
				Sequence: frag.Sequence,
				Detail:   fmt.Sprintf("duplicate mfhd sequence %d", frag.Sequence),
			})
		case frag.Sequence > clk.last+1:
			gap := int(frag.Sequence - clk.last - 1)
			frag.MFHDGap = gap
			r.res.MFHDGaps += gap
			frag.AddIssue(model.Issue{
				Category: model.CategoryMFHDGap,
				Stage:    Stage,
				Offset:   frag.MoofRange.Start,
				Sequence: frag.Sequence,
				Detail: fmt.Sprintf("mfhd sequence gap of %d after %d; no samples fabricated",
					gap, clk.last),
			})
		}
	}
	clk.last = frag.Sequence
	clk.seen = true
	for _, t := range frag.Tracks {
		r.res.LastSequence[t.TrackID] = frag.Sequence
	}
	return nil
}

// Repair is the batch wrapper around Repairer, retained for direct use in
// tests and one-shot callers.
func Repair(set *model.SourceSet) (*Result, error) {
	r, err := NewRepairer(set)
	if err != nil {
		return nil, err
	}
	for i := range set.Fragments {
		if err := r.Apply(&set.Fragments[i]); err != nil {
			return nil, err
		}
	}
	return r.res, nil
}

func repairTrack(frag *model.Fragment, ft *model.FragmentTrack, clk *trackClock, res *Result) error {
	// A traf with no samples carries no media: it cannot contribute an index
	// entry or advance the track clock, so it is left untouched. This is legal
	// in principle (tfhd+tfdt with an empty trun), so it is not an error.
	if len(ft.Samples) == 0 {
		return nil
	}
	// Determine the nominal sample duration for relocation.
	nominal := ft.Samples[0].Duration
	if nominal == 0 {
		nominal = ft.DefaultDuration
	}
	if nominal == 0 {
		return model.NewTaskError(model.CategoryInvalidBox, Stage,
			fmt.Sprintf("fragment %d track %d has no sample duration to relocate", frag.Index, ft.TrackID)).
			WithTrack(ft.TrackID)
	}

	original := ft.Tfdt
	start := ft.Tfdt
	missing := !ft.TfdtPresent
	// A regression is any tfdt that starts before the previous fragment's
	// end: contiguous media starts exactly at the previous end, and a gap
	// starts later, but nothing legitimate starts earlier.
	regressed := ft.TfdtPresent && clk.initialised && ft.Tfdt < clk.lastEnd
	switch {
	case missing:
		start = clk.lastEnd
		ft.Repaired = true
		res.RepairedTfdt++
		res.Repairs = append(res.Repairs, model.RepairRecord{
			Kind: "tfdt_missing", TrackID: ft.TrackID, Sequence: ft.Sequence,
			OriginalValue: 0, RepairedValue: start,
			SourceRange: frag.MoofRange,
			Detail:      "tfdt absent; positioned at previous fragment end",
		})
	case regressed:
		start = clk.lastEnd
		ft.TfdtRegressed = true
		ft.Repaired = true
		res.RepairedTfdt++
		res.Repairs = append(res.Repairs, model.RepairRecord{
			Kind: "tfdt_regression", TrackID: ft.TrackID, Sequence: ft.Sequence,
			OriginalValue: original, RepairedValue: start,
			SourceRange: frag.MoofRange,
			Detail: fmt.Sprintf("tfdt %d regressed below previous end %d; relocated to %d",
				original, clk.lastEnd, start),
		})
	}
	ft.Tfdt = start
	ft.StartTicks = start

	// Walk samples so each gets a monotonic decode time.
	cursor := start
	for si := range ft.Samples {
		s := &ft.Samples[si]
		if s.Duration == 0 {
			s.Duration = nominal
		}
		s.DecodeTicks = cursor
		cursor += uint64(s.Duration)
	}
	ft.EndTicks = cursor
	ft.DurationTicks = cursor - start
	if ft.DurationTicks == 0 {
		return model.NewTaskError(model.CategoryInvalidBox, Stage,
			fmt.Sprintf("fragment %d track %d has zero duration", frag.Index, ft.TrackID)).
			WithTrack(ft.TrackID)
	}

	startMicros, err := model.TicksToMicros(start, clk.timescale)
	if err != nil {
		return err
	}
	endMicros, err := model.TicksToMicros(cursor, clk.timescale)
	if err != nil {
		return err
	}
	ft.StartMicros = startMicros
	ft.EndMicros = endMicros
	for si := range ft.Samples {
		m, err := model.TicksToMicros(ft.Samples[si].DecodeTicks, clk.timescale)
		if err != nil {
			return err
		}
		ft.Samples[si].DecodeMicros = m
	}

	clk.initialised = true
	clk.lastStart = start
	if cursor > clk.lastEnd || !clk.hasEnd {
		clk.lastEnd = cursor
		clk.hasEnd = true
	}
	return nil
}
