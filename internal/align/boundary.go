// Package align converts plan segments into exact millisecond boundaries and
// checks per-representation timeline integrity plus cross-rendition alignment.
package align

import (
	"errors"
	"fmt"
	"sort"

	"dashgen/internal/plan"
	"dashgen/internal/timebase"
)

// Error kinds returned to callers (the CLI maps them to exit code 3).
const (
	KindOutOfOrder = "out_of_order"
	KindOverlap    = "overlap"
	KindMissing    = "missing_segment"
)

// TimelineError describes one structural timeline defect.
type TimelineError struct {
	Kind   string
	RepID  string
	Number int64
	Msg    string
}

func (e *TimelineError) Error() string {
	return fmt.Sprintf("align: %s: rep %q segment %d: %s", e.Kind, e.RepID, e.Number, e.Msg)
}

// MissingError lists a segment number that some representations do not carry.
type MissingError struct {
	Number  int64
	Present []string
	Missing []string
}

func (e *MissingError) Error() string {
	return fmt.Sprintf("align: %s: segment %d present in %v, missing in %v",
		KindMissing, e.Number, e.Present, e.Missing)
}

// SegmentBounds is one segment expressed in exact millisecond rationals.
type SegmentBounds struct {
	Number   int64
	StartMs  timebase.Rational
	Duration timebase.Rational
	EndMs    timebase.Rational
	// Gap is true when this segment does not begin exactly where the previous
	// segment ended (used by SegmentTimeline compression).
	Gap bool
}

// RepTimeline is the converted, order-checked timeline of one representation.
type RepTimeline struct {
	RepID     string
	Timescale int64
	Bounds    []SegmentBounds
}

// Report holds converted timelines and the cross-representation metrics.
type Report struct {
	Timelines        []RepTimeline
	MaxStartOffset   timebase.Rational
	MaxDurationDelta timebase.Rational
	SegmentNumbers   []int64 // sorted union of all segment numbers
}

// Build converts the plan, checks each timeline for reverse ordering and
// overlaps (exactly, in native ticks), then compares boundaries of equally
// numbered segments across representations using millisecond rationals.
// Structural defects are joined into the returned error; the report is still
// returned so callers can emit partial diagnostics.
func Build(p *plan.Plan) (*Report, error) {
	r := &Report{Timelines: make([]RepTimeline, 0, len(p.Representations))}
	var errs []error
	numbersByRep := make(map[string]map[int64]bool)
	allNumbers := map[int64]bool{}

	for i := range p.Representations {
		rep := &p.Representations[i]
		tl, tlErrs := buildTimeline(rep)
		errs = append(errs, tlErrs...)
		r.Timelines = append(r.Timelines, tl)
		numbersByRep[rep.ID] = map[int64]bool{}
		for _, b := range tl.Bounds {
			numbersByRep[rep.ID][b.Number] = true
			allNumbers[b.Number] = true
		}
	}

	numbers := make([]int64, 0, len(allNumbers))
	for n := range allNumbers {
		numbers = append(numbers, n)
	}
	sort.Slice(numbers, func(a, b int) bool { return numbers[a] < numbers[b] })
	r.SegmentNumbers = numbers

	for _, n := range numbers {
		var present, missing []string
		for i := range p.Representations {
			id := p.Representations[i].ID
			if numbersByRep[id][n] {
				present = append(present, id)
			} else {
				missing = append(missing, id)
			}
		}
		if len(missing) > 0 {
			errs = append(errs, &MissingError{Number: n, Present: present, Missing: missing})
		}
	}

	computeMetrics(r)
	return r, errors.Join(errs...)
}

func buildTimeline(rep *plan.Representation) (RepTimeline, []error) {
	tl := RepTimeline{RepID: rep.ID, Timescale: rep.Timescale}
	var errs []error
	var prevEndTicks int64
	for i := range rep.Segments {
		seg := &rep.Segments[i]
		if i > 0 {
			prev := &rep.Segments[i-1]
			prevEnd, _ := timebase.Add64(prev.StartTicks, prev.DurationTicks)
			prevEndTicks = prevEnd
			switch {
			case seg.StartTicks < prev.StartTicks:
				errs = append(errs, &TimelineError{
					Kind: KindOutOfOrder, RepID: rep.ID, Number: seg.Number,
					Msg: fmt.Sprintf("start %d is before previous start %d", seg.StartTicks, prev.StartTicks),
				})
			case seg.StartTicks < prevEnd:
				errs = append(errs, &TimelineError{
					Kind: KindOverlap, RepID: rep.ID, Number: seg.Number,
					Msg: fmt.Sprintf("start %d overlaps previous end %d", seg.StartTicks, prevEnd),
				})
			}
		}
		startMs, err := timebase.TicksToMs(seg.StartTicks, rep.Timescale)
		if err != nil {
			errs = append(errs, &TimelineError{Kind: KindOverlap, RepID: rep.ID, Number: seg.Number, Msg: err.Error()})
			continue
		}
		dur, err := timebase.TicksToMs(seg.DurationTicks, rep.Timescale)
		if err != nil {
			errs = append(errs, &TimelineError{Kind: KindOverlap, RepID: rep.ID, Number: seg.Number, Msg: err.Error()})
			continue
		}
		end, err := addRational(startMs, dur)
		if err != nil {
			errs = append(errs, &TimelineError{Kind: KindOverlap, RepID: rep.ID, Number: seg.Number, Msg: err.Error()})
			continue
		}
		tl.Bounds = append(tl.Bounds, SegmentBounds{
			Number:   seg.Number,
			StartMs:  startMs,
			Duration: dur,
			EndMs:    end,
			Gap:      i > 0 && seg.StartTicks != prevEndTicks,
		})
	}
	return tl, errs
}

func addRational(a, b timebase.Rational) (timebase.Rational, error) {
	num, ok1 := timebase.Mul64(a.Num, b.Den)
	num2, ok2 := timebase.Mul64(b.Num, a.Den)
	if !ok1 || !ok2 {
		return timebase.Rational{}, fmt.Errorf("rational addition overflow")
	}
	sum, ok := timebase.Add64(num, num2)
	if !ok {
		return timebase.Rational{}, fmt.Errorf("rational addition overflow")
	}
	den, ok := timebase.Mul64(a.Den, b.Den)
	if !ok {
		return timebase.Rational{}, fmt.Errorf("rational addition overflow")
	}
	return timebase.Make(sum, den)
}

func computeMetrics(r *Report) {
	byNumber := map[int64][]SegmentBounds{}
	for _, tl := range r.Timelines {
		for _, b := range tl.Bounds {
			byNumber[b.Number] = append(byNumber[b.Number], b)
		}
	}
	var maxStart, maxDur timebase.Rational
	found := false
	for _, n := range r.SegmentNumbers {
		group := byNumber[n]
		if len(group) != len(r.Timelines) {
			continue // missing segments already reported
		}
		for i := 0; i < len(group); i++ {
			for j := i + 1; j < len(group); j++ {
				sdiff, err := group[i].StartMs.Sub(group[j].StartMs)
				if err != nil {
					continue
				}
				ddiff, err := group[i].Duration.Sub(group[j].Duration)
				if err != nil {
					continue
				}
				sdiff, ddiff = sdiff.Abs(), ddiff.Abs()
				if !found {
					maxStart, maxDur, found = sdiff, ddiff, true
					continue
				}
				if sdiff.Cmp(maxStart) > 0 {
					maxStart = sdiff
				}
				if ddiff.Cmp(maxDur) > 0 {
					maxDur = ddiff
				}
			}
		}
	}
	if found {
		r.MaxStartOffset = maxStart
		r.MaxDurationDelta = maxDur
	}
}
