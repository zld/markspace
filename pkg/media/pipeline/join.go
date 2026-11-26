package pipeline

import (
	"sort"

	"cmafrepair/pkg/media/clock"
	"cmafrepair/pkg/media/cmaf"
	"cmafrepair/pkg/media/timebase"
)

// BoundaryMismatch describes one cross-rendition boundary that failed the
// one-tick alignment check.
type BoundaryMismatch struct {
	Sequence       uint32
	Representation string
	ReferenceMs    int64
	GotMs          int64
	DeltaMs        int64
	Reason         string
}

// JoinReport is the fan-in result: per-boundary reference times on the shared
// clock plus every mismatch and the representations that were isolated.
type JoinReport struct {
	// ReferenceMs maps sequence number to the shared-clock boundary chosen
	// from the first healthy representation.
	ReferenceMs map[uint32]int64
	Mismatches  []BoundaryMismatch
	Isolated    []string
	// TotalBoundaries counts sequence positions examined.
	TotalBoundaries int
	// Wraps sums RTP wrap events across all representations.
	Wraps int
}

// Join fans repaired representations into the shared clock and verifies that
// corresponding fragment boundaries (matched by media sequence) agree within
// one clock tick. Divergent representations are isolated for the offending
// boundary while healthy ones continue to define the reference.
func Join(results []RepResult) (*JoinReport, error) {
	cal := clock.NewCalibrator()
	report := &JoinReport{ReferenceMs: map[uint32]int64{}}
	isolatedSet := map[string]struct{}{}
	healthy := make([]RepResult, 0, len(results))

	for _, r := range results {
		report.Wraps += r.Wraps
		if r.Fatal != nil || r.Timeline == nil {
			isolatedSet[r.Name] = struct{}{}
			report.Mismatches = append(report.Mismatches, BoundaryMismatch{
				Representation: r.Name,
				Reason:         fatalReason(r),
			})
			continue
		}
		if err := cal.SetAnchor(r.Name, r.Anchor); err != nil {
			isolatedSet[r.Name] = struct{}{}
			report.Mismatches = append(report.Mismatches, BoundaryMismatch{
				Representation: r.Name,
				Reason:         err.Error(),
			})
			continue
		}
		healthy = append(healthy, r)
	}
	if len(healthy) == 0 {
		return report, timebase.NewError(timebase.CodeClockDivergence, timebase.PhaseClock,
			"no healthy representation available for fan-in")
	}

	// Collect the ordered set of sequence numbers across healthy reps.
	seqSet := map[uint32]struct{}{}
	for _, r := range healthy {
		for _, e := range r.Timeline.Entries {
			seqSet[e.Sequence] = struct{}{}
		}
	}
	seqs := make([]uint32, 0, len(seqSet))
	for s := range seqSet {
		seqs = append(seqs, s)
	}
	sort.Slice(seqs, func(i, j int) bool { return seqs[i] < seqs[j] })

	for _, seq := range seqs {
		report.TotalBoundaries++
		bounds := make([]clock.Boundary, 0, len(healthy))
		for _, r := range healthy {
			e, ok := findEntry(r.Timeline, seq)
			if !ok {
				// Rendition lacks this sequence; record a mismatch but do not
				// isolate the whole rendition for one missing fragment.
				report.Mismatches = append(report.Mismatches, BoundaryMismatch{
					Sequence:       seq,
					Representation: r.Name,
					Reason:         "representation missing this sequence",
				})
				continue
			}
			bounds = append(bounds, clock.Boundary{
				Representation: r.Name, Sequence: seq, MediaTime: e.FixedTfdt,
			})
		}
		if len(bounds) == 0 {
			continue
		}
		refMs, divs, err := cal.Calibrate(bounds)
		if err != nil {
			return report, err
		}
		report.ReferenceMs[seq] = refMs
		for _, d := range divs {
			report.Mismatches = append(report.Mismatches, BoundaryMismatch{
				Sequence:       seq,
				Representation: d.Representation,
				ReferenceMs:    refMs,
				GotMs:          d.GotMs,
				DeltaMs:        d.DeltaMs,
				Reason:         d.String(),
			})
			isolatedSet[d.Representation] = struct{}{}
		}
	}

	for name := range isolatedSet {
		report.Isolated = append(report.Isolated, name)
	}
	sort.Strings(report.Isolated)
	return report, nil
}

// MismatchCount returns the number of boundary alignment failures.
func (r *JoinReport) MismatchCount() int { return len(r.Mismatches) }

func findEntry(tl *cmaf.Timeline, seq uint32) (cmaf.TimelineEntry, bool) {
	for _, e := range tl.Entries {
		if e.Sequence == seq {
			return e, true
		}
	}
	return cmaf.TimelineEntry{}, false
}

func fatalReason(r RepResult) string {
	if r.Fatal != nil {
		return r.Fatal.Error()
	}
	return "no repaired timeline"
}
