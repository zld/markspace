// Package analyze places parsed segments onto the unified sample timeline,
// localizes gaps and overlaps, and emits deterministic per-segment results.
// The same input always produces the same ordering and conclusions.
package analyze

import (
	"sort"

	"mediad/media"
)

// Segment is one input block projected onto the PCM sample axis.
type Segment struct {
	// InputIndex preserves arrival order and breaks ties in sorting.
	InputIndex  int
	Name        string
	SampleStart int64
	SampleEnd   int64 // half-open
	Bytes       int64 // estimated serialized size, used by the budget stage
	Faults      []media.Fault
}

// Interval is the analyzed form of a segment.
type Interval struct {
	InputIndex   int           `json:"input_index"`
	Name         string        `json:"name"`
	SampleStart  int64         `json:"sample_start"`
	SampleEnd    int64         `json:"sample_end"`
	ExpectedNext int64         `json:"expected_next"`
	ActualNext   int64         `json:"actual_next"`
	ExpectedGap  int64         `json:"expected_gap_samples"`
	ActualGap    int64         `json:"actual_gap_samples"`
	DamageRanges []media.Fault `json:"damage_ranges"`
	QualityPass  bool          `json:"quality_pass"`
	Bytes        int64         `json:"bytes_estimated"`
}

// DamageRange is a summary view of one localized defect on the timeline.
type DamageRange struct {
	Category    media.Category `json:"category"`
	SampleStart int64          `json:"sample_start"`
	SampleEnd   int64          `json:"sample_end"`
}

// Report is the deterministic analysis output for a whole input set.
type Report struct {
	Intervals    []Interval    `json:"intervals"`
	SamplesTotal int64         `json:"samples_total"`
	Damage       []DamageRange `json:"damage_ranges"`
}

// Run sorts the segments by (sample start, input index), computes expected
// versus actual inter-segment spacing, attaches timeline faults and decides
// the per-segment quality gate. It never mutates the input slice order.
func Run(segments []Segment) *Report {
	ordered := make([]Segment, len(segments))
	copy(ordered, segments)
	sort.SliceStable(ordered, func(i, j int) bool {
		if ordered[i].SampleStart != ordered[j].SampleStart {
			return ordered[i].SampleStart < ordered[j].SampleStart
		}
		return ordered[i].InputIndex < ordered[j].InputIndex
	})

	rep := &Report{}
	var total int64
	for i, seg := range ordered {
		iv := Interval{
			InputIndex:   seg.InputIndex,
			Name:         seg.Name,
			SampleStart:  seg.SampleStart,
			SampleEnd:    seg.SampleEnd,
			ExpectedNext: seg.SampleEnd,
			Bytes:        seg.Bytes,
		}
		faults := append([]media.Fault(nil), seg.Faults...)

		if i > 0 {
			prev := ordered[i-1]
			// A healthy timeline is contiguous: the next segment must start
			// exactly where the previous one ended, so the expected gap is 0.
			iv.ExpectedGap = 0
			iv.ActualGap = seg.SampleStart - prev.SampleEnd
			iv.ExpectedNext = prev.SampleEnd
			iv.ActualNext = seg.SampleStart
			switch {
			case iv.ActualGap > 0:
				faults = append(faults, media.NewSampleFault(
					media.CatIntervalGap, prev.SampleEnd, seg.SampleStart,
					"gap of %d samples between %q and %q",
					iv.ActualGap, prev.Name, seg.Name))
			case iv.ActualGap < 0:
				faults = append(faults, media.NewSampleFault(
					media.CatIntervalOverlap, seg.SampleStart, prev.SampleEnd,
					"overlap of %d samples between %q and %q",
					-iv.ActualGap, prev.Name, seg.Name))
			}
		}

		sortFaults(faults)
		iv.DamageRanges = faults
		iv.QualityPass = !media.HasFatal(faults)
		rep.Intervals = append(rep.Intervals, iv)

		for _, flt := range faults {
			s, e := flt.SampleStart, flt.SampleEnd
			if s < 0 { // byte-only faults inherit the segment's sample span
				s, e = seg.SampleStart, seg.SampleEnd
			}
			rep.Damage = append(rep.Damage, DamageRange{
				Category:    flt.Category,
				SampleStart: s,
				SampleEnd:   e,
			})
		}
		total += seg.SampleEnd - seg.SampleStart
	}
	rep.SamplesTotal = total
	sort.SliceStable(rep.Damage, func(i, j int) bool {
		a, b := rep.Damage[i], rep.Damage[j]
		if a.SampleStart != b.SampleStart {
			return a.SampleStart < b.SampleStart
		}
		if a.SampleEnd != b.SampleEnd {
			return a.SampleEnd < b.SampleEnd
		}
		return a.Category < b.Category
	})
	return rep
}

// sortFaults gives every fault list a canonical order so repeated runs render
// byte-identical JSON: sample anchor first, then byte anchor, then category.
func sortFaults(faults []media.Fault) {
	sort.SliceStable(faults, func(i, j int) bool {
		a, b := faults[i], faults[j]
		if a.SampleStart != b.SampleStart {
			return a.SampleStart < b.SampleStart
		}
		if a.ByteStart != b.ByteStart {
			return a.ByteStart < b.ByteStart
		}
		return a.Category < b.Category
	})
}
