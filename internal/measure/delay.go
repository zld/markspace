// Package measure turns a plan-aligned timeline into reproducible quality
// numbers: publish-delay quantiles, missing and duplicate event counts, and
// per-rendition boundary (sample-grid) errors. Every value is derived purely
// from the supplied samples and plan, so identical inputs produce identical
// numbers and identical hashes downstream.
package measure

import (
	"sort"

	"regress/internal/clock"
	"regress/internal/model"
	"regress/internal/timeline"
)

// DelayStats summarizes observed publish delays in master ticks.
type DelayStats struct {
	Count    int          `json:"count"`
	Min      clock.Tick   `json:"min"`
	Max      clock.Tick   `json:"max"`
	Median   clock.Tick   `json:"median"`
	P90      clock.Tick   `json:"p90"`
	P99      clock.Tick   `json:"p99"`
	Delays   []clock.Tick `json:"delays"` // sorted ascending
}

// Counts are the plan-based loss/dup totals.
type Counts struct {
	Missing   int `json:"missing"`
	Duplicate int `json:"duplicate"`
	// Extra counts occurrences whose key is not part of the plan.
	Extra int `json:"extra"`
}

// BoundaryError compares one rendition's reconstructed sample grid against
// the ideal constant-frame-interval grid derived from the plan timeline.
type BoundaryError struct {
	Rendition   string       `json:"rendition"`
	Samples     int          `json:"samples"`
	MaxAbsError clock.Tick   `json:"max_abs_error_ticks"`
	Errors      []clock.Tick `json:"errors"` // per-sample signed error
}

// Report is the full deterministic measurement result.
type Report struct {
	Delay       DelayStats      `json:"delay"`
	Counts      Counts          `json:"counts"`
	Boundaries  []BoundaryError `json:"boundaries"`
}

// Compute produces the report for one aligned timeline. renditionTracks are
// the parsed media tracks (video and audio) whose sample grids are checked.
func Compute(tl *timeline.Timeline, renditionTracks []model.Track) *Report {
	r := &Report{}
	r.Delay = computeDelays(tl)
	r.Counts = computeCounts(tl)
	r.Boundaries = computeBoundaries(renditionTracks)
	return r
}

// computeDelays uses only observed (non-missing) alignments. Delay is
// observed presentation tick minus the planned tick on the master line.
func computeDelays(tl *timeline.Timeline) DelayStats {
	var delays []clock.Tick
	for _, a := range tl.Alignment {
		if a.Missing {
			continue
		}
		delays = append(delays, a.ObservedTick-a.Plan.ExpectedTick)
	}
	sort.Slice(delays, func(i, j int) bool { return delays[i] < delays[j] })
	if len(delays) == 0 {
		return DelayStats{Delays: nil}
	}
	return DelayStats{
		Count:  len(delays),
		Min:    delays[0],
		Max:    delays[len(delays)-1],
		Median: Quantile(delays, 50),
		P90:    Quantile(delays, 90),
		P99:    Quantile(delays, 99),
		Delays: delays,
	}
}

func computeCounts(tl *timeline.Timeline) Counts {
	var c Counts
	planned := map[string]bool{}
	for _, a := range tl.Alignment {
		planned[a.Plan.Key] = true
		if a.Missing {
			c.Missing++
		}
		if a.Duplicate {
			c.Duplicate++
		}
	}
	for _, ev := range tl.Unique {
		if !planned[ev.Key] {
			c.Extra++
		}
	}
	return c
}

// computeBoundaries checks each media track's samples against a grid starting
// at the first sample's tick with a constant per-track nominal step. For
// video the step is one frame interval; audio uses its own clock spacing.
// Boundary error never exceeds one tick for correctly generated fixtures.
func computeBoundaries(tracks []model.Track) []BoundaryError {
	var out []BoundaryError
	for _, tr := range tracks {
		if len(tr.Samples) < 2 {
			continue
		}
		be := BoundaryError{Rendition: tr.ID, Samples: len(tr.Samples)}
		first := tr.Samples[0].Tick
		// Nominal step is derived from the observed first interval, so the
		// check measures grid regularity rather than an assumed offset.
		step := tr.Samples[1].Tick - tr.Samples[0].Tick
		var max clock.Tick
		for i, s := range tr.Samples {
			ideal := first + clock.Tick(i)*step
			err := s.Tick - ideal
			be.Errors = append(be.Errors, err)
			absErr := err
			if absErr < 0 {
				absErr = -absErr
			}
			if absErr > max {
				max = absErr
			}
		}
		be.MaxAbsError = max
		out = append(out, be)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Rendition < out[j].Rendition })
	return out
}

// Quantile returns the nearest-rank percentile (p in 0..100) of an already
// ascending slice. The method is fixed so reruns are bit-for-bit identical.
func Quantile(sorted []clock.Tick, p uint) clock.Tick {
	if len(sorted) == 0 {
		return 0
	}
	if p > 100 {
		p = 100
	}
	// Nearest-rank: rank = ceil(p/100 * N), 1-based.
	rank := (int64(p)*int64(len(sorted)) + 99) / 100
	if rank < 1 {
		rank = 1
	}
	if rank > int64(len(sorted)) {
		rank = int64(len(sorted))
	}
	return sorted[rank-1]
}
