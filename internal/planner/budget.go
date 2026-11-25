// Package planner chooses bitrate, time precision and segmentation parameters
// for a regression run. It never trades a quality floor for a budget: when no
// candidate clears the floor the plan is refused outright.
package planner

import (
	"fmt"
	"math"
	"sort"

	"rtpregress/internal/media"
)

// Candidate is one point in the planning grid.
type Candidate struct {
	BitrateKbps int `json:"bitrateKbps"`
	PrecisionUS int `json:"precisionUs"`
	SegmentMS   int `json:"segmentMs"`
}

// String renders the candidate for reports and deterministic tie-breaking.
func (c Candidate) String() string {
	return fmt.Sprintf("bitrate=%dkbps precision=%dus segment=%dms",
		c.BitrateKbps, c.PrecisionUS, c.SegmentMS)
}

// Constraints are the hard limits supplied by the operator.
type Constraints struct {
	BudgetMS   float64 `json:"budgetMs"`
	MaxBytes   int     `json:"maxBytes"`
	QualityMin float64 `json:"qualityMin"`
}

// Problem summarises the work a plan must cover.
type Problem struct {
	Events     int   `json:"events"`
	TextBytes  int   `json:"textBytes"`
	DurationNs int64 `json:"durationNs"`
}

// Plan is the selected candidate together with its measured estimates.
type Plan struct {
	Candidate
	Quality        float64 `json:"quality"`
	CostMS         float64 `json:"costMs"`
	Segments       int     `json:"segments"`
	EstimatedBytes int     `json:"estimatedBytes"`
}

// Quality scores a candidate in [0,1]. Bitrate saturates at 256 kbps and
// precision at 200µs; the two halves are weighted 55/45 so neither can carry
// the floor alone.
func Quality(c Candidate) float64 {
	bitrateScore := math.Min(1, float64(c.BitrateKbps)/256)
	precisionScore := 1 - math.Min(1, float64(c.PrecisionUS)/200)
	return 0.55*bitrateScore + 0.45*precisionScore
}

// SegmentCount returns how many output segments cover the duration.
func SegmentCount(durationNs int64, segmentMS int) int {
	if segmentMS <= 0 {
		return 1
	}
	if durationNs <= 0 {
		return 1
	}
	n := int(math.Ceil(float64(durationNs) / float64(int64(segmentMS)*1_000_000)))
	if n < 1 {
		return 1
	}
	return n
}

// CostMS estimates processing time. It grows with event count, segment count
// and time precision (finer precision means more records to touch).
func CostMS(p Problem, c Candidate) float64 {
	segs := SegmentCount(p.DurationNs, c.SegmentMS)
	return 4 + float64(p.Events)*0.2 + float64(segs)*1.0 +
		float64(p.Events)*(100.0/float64(c.PrecisionUS))*0.2
}

// EstimatedBytes predicts the size of the generated artifact. It matches the
// artifact layout in verify: a 48-byte header, a 16-byte record per segment,
// 20 bytes per event record and the event text.
func EstimatedBytes(p Problem, c Candidate) int {
	segs := SegmentCount(p.DurationNs, c.SegmentMS)
	return 48 + segs*16 + p.Events*20 + p.TextBytes
}

// DefaultCandidates is the fixed planning grid. Fixed rather than random so
// two runs of the same input choose the same plan.
func DefaultCandidates() []Candidate {
	var out []Candidate
	for _, br := range []int{64, 128, 192, 256} {
		for _, pr := range []int{100, 50, 20, 10} {
			for _, seg := range []int{100, 250, 500, 1000} {
				out = append(out, Candidate{BitrateKbps: br, PrecisionUS: pr, SegmentMS: seg})
			}
		}
	}
	return out
}

// Choose selects a plan in the documented order: clear the quality floor
// first, then fit the processing budget, then fit the byte ceiling. Among
// survivors the highest quality wins, with cost, bytes, precision, bitrate and
// segment length as deterministic tie-breakers. Refusal is a structured
// budget error that names the binding constraint.
func Choose(p Problem, cons Constraints) (*Plan, error) {
	if cons.MaxBytes <= 0 {
		return nil, media.Newf(media.KindBudget, "max-bytes must be positive, got %d", cons.MaxBytes)
	}
	if cons.BudgetMS < 0 {
		return nil, media.Newf(media.KindBudget, "budget-ms must be non-negative, got %.1f", cons.BudgetMS)
	}
	if cons.QualityMin < 0 || cons.QualityMin > 1 {
		return nil, media.Newf(media.KindBudget, "quality-min must be in [0,1], got %.3f", cons.QualityMin)
	}

	cands := DefaultCandidates()
	bestQuality := 0.0
	for _, c := range cands {
		if q := Quality(c); q > bestQuality {
			bestQuality = q
		}
	}

	// Stage 1: quality floor. This filter is never relaxed.
	var passQuality []Candidate
	for _, c := range cands {
		if Quality(c) >= cons.QualityMin {
			passQuality = append(passQuality, c)
		}
	}
	if len(passQuality) == 0 {
		return nil, media.Newf(media.KindBudget,
			"no candidate meets quality floor %.3f (best achievable %.3f); refusing to degrade",
			cons.QualityMin, bestQuality)
	}

	// Stage 2: processing budget, then stage 3: delivered byte ceiling.
	var survivors []Plan
	var budgetRejected, bytesRejected int
	for _, c := range passQuality {
		cost := CostMS(p, c)
		bytes := EstimatedBytes(p, c)
		if cost > cons.BudgetMS {
			budgetRejected++
			continue
		}
		if bytes > cons.MaxBytes {
			bytesRejected++
			continue
		}
		survivors = append(survivors, Plan{
			Candidate:      c,
			Quality:        Quality(c),
			CostMS:         cost,
			Segments:       SegmentCount(p.DurationNs, c.SegmentMS),
			EstimatedBytes: bytes,
		})
	}
	if len(survivors) == 0 {
		return nil, media.Newf(media.KindBudget,
			"no candidate fits after the quality floor: %d exceeded budget %.1fms, %d exceeded max-bytes %d",
			budgetRejected, cons.BudgetMS, bytesRejected, cons.MaxBytes)
	}

	sort.Slice(survivors, func(i, j int) bool {
		a, b := survivors[i], survivors[j]
		if a.Quality != b.Quality {
			return a.Quality > b.Quality
		}
		if a.CostMS != b.CostMS {
			return a.CostMS < b.CostMS
		}
		if a.EstimatedBytes != b.EstimatedBytes {
			return a.EstimatedBytes < b.EstimatedBytes
		}
		if a.PrecisionUS != b.PrecisionUS {
			return a.PrecisionUS < b.PrecisionUS
		}
		if a.BitrateKbps != b.BitrateKbps {
			return a.BitrateKbps > b.BitrateKbps
		}
		return a.SegmentMS < b.SegmentMS
	})
	best := survivors[0]
	return &best, nil
}

// QuantizeNs rounds a nanosecond time to the plan's precision grid and
// returns nanoseconds. Half-steps round up, so the quantization error never
// exceeds half the precision.
func (p *Plan) QuantizeNs(ns int64) int64 {
	step := int64(p.PrecisionUS) * 1000
	q := ns / step
	rem := ns % step
	if rem*2 >= step {
		q++
	}
	return q * step
}

// QuantizeDeviationNs reports the largest possible error for the plan's
// precision, in nanoseconds.
func (p *Plan) QuantizeDeviationNs() int64 {
	return int64(p.PrecisionUS) * 1000 / 2
}
