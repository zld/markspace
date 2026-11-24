// Package align creates manifests only at boundaries shared by every
// representation. Missing or mismatched boundaries become explicit alignment
// errors and are never emitted as silently shifted segments.
package align

import (
	"fmt"
	"sort"

	"github.com/zld/avpipe/internal/clock"
	"github.com/zld/avpipe/internal/mp4"
)

// Boundary is one representation's fragment boundary.
type Boundary struct {
	Representation string          `json:"representation"`
	Sequence       uint32          `json:"sequence"`
	Start          clock.Timestamp `json:"start"`
	End            clock.Timestamp `json:"end"`
}

// AlignedSegment is a common boundary accepted for the manifest.
type AlignedSegment struct {
	Sequence        uint32          `json:"sequence"`
	Start           clock.Timestamp `json:"start"`
	End             clock.Timestamp `json:"end"`
	StartMillis     int64           `json:"start_ms"`
	DurationMillis  int64           `json:"duration_ms"`
	Representations []string        `json:"representations"`
	Boundaries      []Boundary      `json:"boundaries"`
}

// Error describes a boundary that could not be aligned.
type Error struct {
	Sequence               uint32   `json:"sequence"`
	Code                   string   `json:"code"`
	MissingRepresentations []string `json:"missing_representations,omitempty"`
	Message                string   `json:"message"`
}

// Report contains aligned segments and deterministic alignment failures.
type Report struct {
	Timescale uint32           `json:"timescale"`
	Segments  []AlignedSegment `json:"segments"`
	Errors    []Error          `json:"alignment_errors"`
}

type indexedSegment struct {
	report  *mp4.Report
	segment mp4.Segment
}

// Align intersects readable sequence boundaries across reports.
func Align(reports []*mp4.Report, targetTimescale uint32) *Report {
	if targetTimescale == 0 {
		targetTimescale = 1000
	}
	out := &Report{Timescale: targetTimescale}
	if len(reports) == 0 {
		out.Errors = append(out.Errors, Error{Code: "no_representations", Message: "no representation reports supplied"})
		return out
	}

	bySequence := make(map[uint32]map[string]indexedSegment)
	repOrder := make([]string, 0, len(reports))
	seenRep := make(map[string]struct{})
	sequences := make(map[uint32]struct{})

	for _, rr := range reports {
		if rr == nil {
			out.Errors = append(out.Errors, Error{Code: "nil_representation", Message: "nil MP4 report"})
			continue
		}
		repID := rr.Representation
		if _, ok := seenRep[repID]; !ok {
			seenRep[repID] = struct{}{}
			repOrder = append(repOrder, repID)
		}
		for _, seg := range rr.Segments {
			if bySequence[seg.Sequence] == nil {
				bySequence[seg.Sequence] = make(map[string]indexedSegment)
			}
			if existing, dup := bySequence[seg.Sequence][repID]; dup {
				out.Errors = append(out.Errors, Error{
					Sequence: seg.Sequence, Code: "duplicate_boundary",
					Message: fmt.Sprintf("duplicate sequence in %s at offsets %d and %d",
						repID, existing.segment.BoxOffset, seg.BoxOffset),
				})
				continue
			}
			bySequence[seg.Sequence][repID] = indexedSegment{report: rr, segment: seg}
			sequences[seg.Sequence] = struct{}{}
		}
	}

	seqList := make([]uint32, 0, len(sequences))
	for seq := range sequences {
		seqList = append(seqList, seq)
	}
	sort.Slice(seqList, func(i, j int) bool { return seqList[i] < seqList[j] })

	for _, seq := range seqList {
		entries := bySequence[seq]
		var missing []string
		var boundaries []Boundary
		var baseStart, baseEnd clock.Timestamp
		haveBase := false
		mismatch := false

		for _, repID := range repOrder {
			entry, ok := entries[repID]
			if !ok || !entry.segment.Readable {
				missing = append(missing, repID)
				continue
			}
			s := entry.segment
			start := clock.NewTimestamp(clock.Ticks(s.StartTicks), s.Timescale)
			end := clock.NewTimestamp(clock.Ticks(s.StartTicks+int64(s.DurationTicks)), s.Timescale)
			boundaries = append(boundaries, Boundary{
				Representation: repID, Sequence: seq, Start: start, End: end,
			})
			if !haveBase {
				baseStart, baseEnd = start, end
				haveBase = true
				continue
			}
			if clock.Compare(baseStart, start) != 0 || clock.Compare(baseEnd, end) != 0 {
				mismatch = true
				out.Errors = append(out.Errors, Error{
					Sequence: seq, Code: "boundary_mismatch",
					Message: fmt.Sprintf("boundary mismatch: %s=%s..%s vs %s=%s..%s",
						repID, tsString(start), tsString(end), "base", tsString(baseStart), tsString(baseEnd)),
				})
			}
		}

		if len(missing) > 0 {
			out.Errors = append(out.Errors, Error{
				Sequence: seq, Code: "missing_boundary",
				MissingRepresentations: append([]string{}, missing...),
				Message:                fmt.Sprintf("sequence %d lacks a readable boundary in: %v", seq, missing),
			})
			continue
		}
		if mismatch || !haveBase {
			continue
		}

		commonStart := baseStart.In(targetTimescale)
		commonEnd := baseEnd.In(targetTimescale)
		out.Segments = append(out.Segments, AlignedSegment{
			Sequence:        seq,
			Start:           commonStart,
			End:             commonEnd,
			StartMillis:     int64(commonStart.In(1000).Ticks),
			DurationMillis:  int64(commonEnd.Ticks-commonStart.Ticks) * 1000 / int64(targetTimescale),
			Representations: append([]string{}, repOrder...),
			Boundaries:      boundaries,
		})
	}

	sort.Slice(out.Errors, func(i, j int) bool {
		if out.Errors[i].Sequence != out.Errors[j].Sequence {
			return out.Errors[i].Sequence < out.Errors[j].Sequence
		}
		return out.Errors[i].Code < out.Errors[j].Code
	})
	return out
}

func tsString(ts clock.Timestamp) string {
	return fmt.Sprintf("%d/%d", ts.Ticks, ts.Timescale)
}
