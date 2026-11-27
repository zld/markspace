// Package damage localizes damaged stream regions onto the unified rational
// time base. It consumes the same fault model and ordered samples as the
// structural inspector, so a fault's time range and its first error offset
// always agree with the structure report.
package damage

import (
	"sort"

	"media-check/internal/clock"
	"media-check/internal/fault"
	"media-check/internal/stream"
)

// Segment is one contiguous damaged media-time region.
type Segment struct {
	TrackID uint32
	// Start/End are rational media times bounding the segment.
	Start clock.Rational
	End   clock.Rational
	// FirstErrOffset is the earliest byte offset reported inside the region.
	FirstErrOffset int64
	// SeqStart/SeqEnd are the original extended packet sequence range.
	SeqStart uint64
	SeqEnd   uint64
	Codes    []fault.Code
	// RecoveredUnits counts parseable media units rebuilt around the damage.
	RecoveredUnits int
	// MissingUnits counts units that could not be rebuilt.
	MissingUnits int
}

// RecoveryRate is the bounded 0..1 measure defined as
// rebuilt parseable units / recoverable missing units.
type RecoveryRate struct {
	Recovered   int
	Recoverable int
	Rate        float64
}

// Locator turns faults + samples into time-anchored damage segments.
type Locator struct {
	faults *fault.Model
}

// NewLocator binds the shared fault model.
func NewLocator(fm *fault.Model) *Locator { return &Locator{faults: fm} }

// Locate groups every non-duplicate fault into contiguous per-track segments.
// Sample times supply the media-time bounds; sequence bounds come straight
// from the faults so even wholly missing units retain a packet reference.
func (l *Locator) Locate(rep *stream.Report) []Segment {
	byTrack := map[uint32][]fault.Fault{}
	for _, f := range l.faults.All() {
		if f.Code == fault.CodeDuplicate {
			continue // duplicates are handled transparently, not damage
		}
		byTrack[f.TrackID] = append(byTrack[f.TrackID], f)
	}
	var ids []uint32
	for id := range byTrack {
		ids = append(ids, id)
	}
	sort.Slice(ids, func(i, j int) bool { return ids[i] < ids[j] })

	var out []Segment
	for _, id := range ids {
		fs := byTrack[id]
		sort.SliceStable(fs, func(i, j int) bool {
			if fs[i].SeqStart != fs[j].SeqStart {
				return fs[i].SeqStart < fs[j].SeqStart
			}
			return fs[i].FirstErrOffset < fs[j].FirstErrOffset
		})
		trackSegs := mergeFaults(id, fs, rep)
		classifyRecovery(trackSegs, rep.ByTrackID(id))
		out = append(out, trackSegs...)
	}
	return out
}

// classifyRecovery counts, per loss segment, how many missing units the
// parsed sample stream can bound. A gap is recoverable when a surviving
// sample exists immediately before and after the whole run: the timeline can
// then be rebuilt around it without inventing media. A gap touching the
// stream edge (no neighbor on one side) cannot be fully bounded.
func classifyRecovery(segs []Segment, tr *stream.TrackReport) {
	if tr == nil {
		return
	}
	present := make(map[uint64]bool, len(tr.Samples))
	for _, sm := range tr.Samples {
		present[sm.SeqStart] = true
	}
	for i := range segs {
		s := &segs[i]
		isLoss := false
		for _, c := range s.Codes {
			if c == fault.CodeLost {
				isLoss = true
			}
		}
		if !isLoss {
			continue
		}
		span := int(s.SeqEnd-s.SeqStart) + 1
		if present[s.SeqStart-1] && present[s.SeqEnd+1] {
			s.RecoveredUnits = span
		} else {
			s.MissingUnits = span
		}
	}
}

// AggregateRecovery derives the recovery rate over all loss segments. The
// denominator is every recoverable missing unit (neighbors survive), the
// numerator is how many of them the pipeline actually bounded.
func AggregateRecovery(segs []Segment) RecoveryRate {
	rec, total := 0, 0
	for _, s := range segs {
		rec += s.RecoveredUnits
		total += s.RecoveredUnits + s.MissingUnits
	}
	return ComputeRecoveryRate(rec, total)
}

// mergeFaults folds faults whose sequence ranges touch or overlap into one
// segment, merging their codes and retaining the earliest error offset.
func mergeFaults(trackID uint32, fs []fault.Fault, rep *stream.Report) []Segment {
	var segs []Segment
	for _, f := range fs {
		if len(segs) > 0 && f.SeqStart <= segs[len(segs)-1].SeqEnd+1 {
			extend(&segs[len(segs)-1], f)
			continue
		}
		segs = append(segs, Segment{
			TrackID:        trackID,
			FirstErrOffset: f.FirstErrOffset,
			SeqStart:       f.SeqStart,
			SeqEnd:         f.SeqEnd,
			Codes:          []fault.Code{f.Code},
		})
	}
	// Attach media times from the surrounding parsed samples.
	samples := rep.ByTrackID(trackID)
	for i := range segs {
		segs[i].Start, segs[i].End = timeBounds(segs[i], samples)
	}
	return segs
}

func extend(s *Segment, f fault.Fault) {
	if f.SeqEnd > s.SeqEnd {
		s.SeqEnd = f.SeqEnd
	}
	if f.FirstErrOffset >= 0 && (s.FirstErrOffset < 0 || f.FirstErrOffset < s.FirstErrOffset) {
		s.FirstErrOffset = f.FirstErrOffset
	}
	for _, c := range s.Codes {
		if c == f.Code {
			return
		}
	}
	s.Codes = append(s.Codes, f.Code)
}

// timeBounds maps a segment to media time: the nearest parsed sample at or
// before the start seq, and the nearest at or after the end seq. Missing
// boundaries fall back to the closest available sample so the range is still
// reported, never silently dropped.
func timeBounds(s Segment, tr *stream.TrackReport) (clock.Rational, clock.Rational) {
	if tr == nil || len(tr.Samples) == 0 {
		return clock.Zero(), clock.Zero()
	}
	start, end := tr.Samples[0].Time, tr.Samples[len(tr.Samples)-1].Time
	for _, sm := range tr.Samples {
		if sm.SeqStart <= s.SeqStart {
			start = sm.Time
		}
		if sm.SeqEnd >= s.SeqEnd {
			end = sm.Time
			break
		}
	}
	return start, end
}

// ComputeRecoveryRate applies the agreed definition: successfully rebuilt
// parseable media units adjacent to loss, divided by the recoverable missing
// units supplied by the fixture ground truth. The result is clamped to [0,1].
func ComputeRecoveryRate(recovered, recoverable int) RecoveryRate {
	if recoverable <= 0 {
		return RecoveryRate{Recovered: 0, Recoverable: 0, Rate: 0}
	}
	rate := float64(recovered) / float64(recoverable)
	if rate < 0 {
		rate = 0
	}
	if rate > 1 {
		rate = 1
	}
	return RecoveryRate{Recovered: recovered, Recoverable: recoverable, Rate: rate}
}

// SegmentSummary is a deterministic textual digest used for the repeat-run
// consistency assertion (same seed must produce identical damage output).
type SegmentSummary struct {
	TrackID        uint32
	StartMillis    int64
	EndMillis      int64
	FirstErrOffset int64
	SeqStart       uint64
	SeqEnd         uint64
	Codes          string
}

// Summarize renders segments into a stable, comparable digest.
func Summarize(segs []Segment) []SegmentSummary {
	out := make([]SegmentSummary, 0, len(segs))
	for _, s := range segs {
		codes := ""
		for i, c := range s.Codes {
			if i > 0 {
				codes += ","
			}
			codes += string(c)
		}
		out = append(out, SegmentSummary{
			TrackID:        s.TrackID,
			StartMillis:    s.Start.Millis(),
			EndMillis:      s.End.Millis(),
			FirstErrOffset: s.FirstErrOffset,
			SeqStart:       s.SeqStart,
			SeqEnd:         s.SeqEnd,
			Codes:          codes,
		})
	}
	return out
}
