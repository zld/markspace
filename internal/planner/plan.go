// Package planner selects render parameters from a discrete candidate set
// under explicit constraints. It never silently degrades: if no candidate fits,
// it reports the conflict in the documented priority order (quality floor,
// processing budget, size).
package planner

import (
	"fmt"
	"sort"

	"medialab/internal/model"
)

// Candidate is one point in the parameter grid.
type Candidate struct {
	// Quality is the pixel-fidelity score in (0,1]; 1 is lossless.
	Quality float64 `json:"quality"`
	// QuantStep is the quantisation step implied by Quality.
	QuantStep int `json:"quantStep"`
	// FPS is the output frame rate divisor: 1 keeps every frame, 2 keeps every
	// second, and so on.
	FPS int `json:"fps"`
	// Frames is the resulting output frame count.
	Frames int `json:"frames"`
	// Bytes is the estimated total artefact size.
	Bytes int64 `json:"bytes"`
	// CostMS is the estimated processing time.
	CostMS int64 `json:"costMs"`
}

// Budget is the hard envelope.
type Budget struct {
	MaxBytes   int64   `json:"maxBytes"`
	MaxCostMS  int64   `json:"maxCostMs"`
	MinQuality float64 `json:"minQuality"`
	// FPSChoices and QualityChoices define the search grid.
	FPSChoices     []int     `json:"fpsChoices"`
	QualityChoices []float64 `json:"qualityChoices"`
}

// Input describes the work the planner must schedule.
type Input struct {
	// BaseBytesPerFrame is the encoded bytes of one frame at quality 1.
	BaseBytesPerFrame int64 `json:"baseBytesPerFrame"`
	// BaseCostMS is the processing cost of one frame at quality 1.
	BaseCostMS int64 `json:"baseCostMs"`
	// TotalFrames is the number of source frames.
	TotalFrames int `json:"totalFrames"`
	// OverheadBytes is the fixed container and manifest cost.
	OverheadBytes int64 `json:"overheadBytes"`
}

// Conflict explains why no candidate is feasible, in priority order.
type Conflict struct {
	// Reason is "quality_floor", "budget" or "size", chosen by priority.
	Reason string `json:"reason"`
	// Detail is a human-readable summary.
	Detail string `json:"detail"`
	// Evaluated lists the candidates that were considered.
	Evaluated []Candidate `json:"evaluated"`
	// BestQuality is the highest quality any candidate achieved.
	BestQuality float64 `json:"bestQuality"`
}

// Plan is the planner's decision.
type Plan struct {
	Chosen   *Candidate  `json:"chosen,omitempty"`
	Feasible []Candidate `json:"feasible"`
	Rejected int         `json:"rejected"`
	Budget   Budget      `json:"budget"`
	Conflict *Conflict   `json:"conflict,omitempty"`
}

// ErrConflict is returned when the grid has no feasible point.
type ErrConflict struct{ Conflict *Conflict }

func (e *ErrConflict) Error() string {
	return fmt.Sprintf("plan conflict (%s): %s", e.Conflict.Reason, e.Conflict.Detail)
}

// Plan evaluates the grid. Feasible candidates must clear the quality floor,
// stay within the processing budget and fit the byte budget; among them the
// smallest byte count wins, ties broken by higher quality, then lower cost.
func Choose(in Input, b Budget) (*Plan, error) {
	if in.TotalFrames <= 0 {
		return nil, model.NewFault(model.KindPlan, model.CodeManifestInvalid, 0, 0,
			"planner needs at least one frame")
	}
	if b.MaxBytes <= 0 || b.MaxCostMS <= 0 {
		return nil, model.NewFault(model.KindPlan, model.CodeBudgetConflict, 0, 0,
			"byte and time budgets must be positive")
	}
	fpsChoices := b.FPSChoices
	if len(fpsChoices) == 0 {
		fpsChoices = []int{1, 2, 4}
	}
	qualityChoices := b.QualityChoices
	if len(qualityChoices) == 0 {
		qualityChoices = []float64{1, 0.875, 0.75, 0.5}
	}

	plan := &Plan{Budget: b}
	floor := 1.0
	for _, q := range qualityChoices {
		step := stepForQuality(q)
		for _, fps := range fpsChoices {
			if fps < 1 {
				continue
			}
			frames := (in.TotalFrames + fps - 1) / fps
			c := Candidate{
				Quality:   quality(q),
				QuantStep: step,
				FPS:       fps,
				Frames:    frames,
				Bytes:     estimateBytes(in, step, frames),
				CostMS:    estimateCost(in, step, frames),
			}
			if c.Quality < floor {
				floor = c.Quality
			}
			reasons := c.reject(b)
			if len(reasons) != 0 {
				plan.Rejected++
				continue
			}
			plan.Feasible = append(plan.Feasible, c)
		}
	}

	if len(plan.Feasible) == 0 {
		plan.Conflict = &Conflict{
			Reason:      firstConflict(in, b, fpsChoices, qualityChoices),
			BestQuality: floor,
			Detail:      conflictDetail(in, b),
		}
		// Include the most informative rejected candidates: the best quality
		// and the smallest size, so the terminal can show why.
		plan.Conflict.Evaluated = sampleAlternatives(in, b, fpsChoices, qualityChoices)
		return plan, &ErrConflict{Conflict: plan.Conflict}
	}

	sort.SliceStable(plan.Feasible, func(i, j int) bool {
		a, c := plan.Feasible[i], plan.Feasible[j]
		if a.Bytes != c.Bytes {
			return a.Bytes < c.Bytes
		}
		if a.Quality != c.Quality {
			return a.Quality > c.Quality
		}
		return a.CostMS < c.CostMS
	})
	chosen := plan.Feasible[0]
	plan.Chosen = &chosen
	return plan, nil
}

// estimateBytes models payload scaling with quality and frame count, plus
// per-frame container overhead.
func estimateBytes(in Input, step, frames int) int64 {
	perFrame := scaledBytes(in.BaseBytesPerFrame, step)
	return in.OverheadBytes + perFrame*int64(frames)
}

// scaledBytes shrinks the payload roughly linearly with quantisation step but
// never below 15% of the base, matching how RLE behaves on synthetic frames.
func scaledBytes(base int64, step int) int64 {
	if step <= 1 {
		return base
	}
	reduction := int64(step-1) * 85 / 2554 // 255 -> 85 percent at step 255
	scaled := base - base*reduction/100
	min := base * 15 / 100
	if scaled < min {
		return min
	}
	return scaled
}

func estimateCost(in Input, step, frames int) int64 {
	// Higher quantisation is slightly cheaper; frame count dominates.
	cost := in.BaseCostMS * int64(frames)
	if step > 1 {
		cost -= cost * int64(step) / 2000
	}
	if cost < 1 {
		cost = 1
	}
	return cost
}

func (c Candidate) reject(b Budget) []string {
	var reasons []string
	if c.Quality < b.MinQuality {
		reasons = append(reasons, "quality_floor")
	}
	if c.CostMS > b.MaxCostMS {
		reasons = append(reasons, "budget")
	}
	if c.Bytes > b.MaxBytes {
		reasons = append(reasons, "size")
	}
	return reasons
}

func quality(q float64) float64 {
	if q <= 0 {
		return 0
	}
	if q > 1 {
		return 1
	}
	return q
}

func stepForQuality(q float64) int {
	if q >= 1 {
		return 1
	}
	s := int(255*(1-q)) + 1
	if s < 1 {
		s = 1
	}
	if s > 255 {
		s = 255
	}
	return s
}

// firstConflict reports the dominant reason: quality floor, then processing
// budget, then size. This order is part of the CLI contract.
func firstConflict(in Input, b Budget, fpsChoices []int, qualityChoices []float64) string {
	bestQuality, cheapestCost, smallestBytes := 0.0, int64(-1), int64(-1)
	for _, q := range qualityChoices {
		step := stepForQuality(q)
		for _, fps := range fpsChoices {
			frames := (in.TotalFrames + fps - 1) / fps
			qq := quality(q)
			if qq > bestQuality {
				bestQuality = qq
			}
			cost := estimateCost(in, step, frames)
			if cheapestCost < 0 || cost < cheapestCost {
				cheapestCost = cost
			}
			bs := estimateBytes(in, step, frames)
			if smallestBytes < 0 || bs < smallestBytes {
				smallestBytes = bs
			}
		}
	}
	if bestQuality < b.MinQuality {
		return model.CodeQualityFloor
	}
	if cheapestCost > b.MaxCostMS {
		return model.CodeBudgetConflict
	}
	if smallestBytes > b.MaxBytes {
		return model.CodeBudgetConflict
	}
	return model.CodeBudgetConflict
}

func conflictDetail(in Input, b Budget) string {
	return fmt.Sprintf("no candidate satisfies min-quality %.3f within %d ms and %d bytes",
		b.MinQuality, b.MaxCostMS, b.MaxBytes)
}

// sampleAlternatives returns up to six representative rejected candidates:
// the highest-quality and the smallest-byte points of the grid.
func sampleAlternatives(in Input, b Budget, fpsChoices []int, qualityChoices []float64) []Candidate {
	var all []Candidate
	for _, q := range qualityChoices {
		step := stepForQuality(q)
		for _, fps := range fpsChoices {
			frames := (in.TotalFrames + fps - 1) / fps
			all = append(all, Candidate{
				Quality: quality(q), QuantStep: step, FPS: fps, Frames: frames,
				Bytes: estimateBytes(in, step, frames), CostMS: estimateCost(in, step, frames),
			})
		}
	}
	sort.SliceStable(all, func(i, j int) bool {
		if all[i].Quality != all[j].Quality {
			return all[i].Quality > all[j].Quality
		}
		return all[i].Bytes < all[j].Bytes
	})
	top := all
	if len(top) > 3 {
		top = top[:3]
	}
	sort.SliceStable(all, func(i, j int) bool {
		if all[i].Bytes != all[j].Bytes {
			return all[i].Bytes < all[j].Bytes
		}
		return all[i].Quality > all[j].Quality
	})
	small := all
	if len(small) > 3 {
		small = small[:3]
	}
	seen := make(map[[3]int64]bool)
	out := make([]Candidate, 0, 6)
	add := func(c Candidate) {
		key := [3]int64{c.Bytes, c.CostMS, int64(c.FPS*1000) + int64(c.QuantStep)}
		if seen[key] {
			return
		}
		seen[key] = true
		out = append(out, c)
	}
	for _, c := range top {
		add(c)
	}
	for _, c := range small {
		add(c)
	}
	return out
}
