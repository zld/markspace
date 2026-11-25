// Package transform applies deterministic budget and quality decisions.
package transform

import (
	"fmt"
	"sort"

	"medpipe/internal/fault"
)

// Constraints are the prioritized acceptance limits.
type Constraints struct {
	// MinQuality is a percentage in [0,100].
	MinQuality float64
	// BudgetMS is the processing-time ceiling.
	BudgetMS int64
	// MaxBytes is the final delivery-size ceiling. Zero means no byte cap.
	MaxBytes int64
}

// Option is one possible transform/delivery profile. Estimates are explicit
// so callers can test priority decisions without running an encoder.
type Option struct {
	Name       string
	Quality    float64
	EstimateMS int64
	Bytes      int64
}

// Decision is the selected profile and the frontier values used to choose it.
type Decision struct {
	Selected          Option
	Quality           float64
	EstimateMS        int64
	OutputBytes       int64
	QualityCandidates int
	BudgetCandidates  int
}

// Decide applies the required conflict order:
//  1. reject profiles below the quality gate;
//  2. among those, retain profiles inside the processing budget;
//  3. among those, choose the smallest delivery size.
//
// It never returns a partial success manifest when the quality gate cannot be
// met within the stated budget.
func Decide(options []Option, c Constraints) (*Decision, error) {
	if c.MinQuality < 0 || c.MinQuality > 100 {
		return nil, fault.New(fault.StageBudget, "invalid_quality_gate", -1, -1,
			fmt.Errorf("min quality %.2f must be in [0,100]", c.MinQuality))
	}
	if c.BudgetMS < 0 {
		return nil, fault.New(fault.StageBudget, "invalid_processing_budget", -1, -1,
			fmt.Errorf("budget-ms %d must be non-negative", c.BudgetMS))
	}
	if c.MaxBytes < 0 {
		return nil, fault.New(fault.StageBudget, "invalid_byte_budget", -1, -1,
			fmt.Errorf("max-bytes %d must be non-negative", c.MaxBytes))
	}

	qualityOK := make([]Option, 0, len(options))
	bestQuality := -1.0
	for _, op := range options {
		if op.Quality > bestQuality {
			bestQuality = op.Quality
		}
		if op.Quality >= c.MinQuality {
			qualityOK = append(qualityOK, op)
		}
	}
	if len(qualityOK) == 0 {
		return nil, fault.New(fault.StageBudget, "quality_gate_unreachable", -1, -1,
			fmt.Errorf("no profile reaches quality %.2f; best=%.2f", c.MinQuality, bestQuality))
	}

	withinBudget := make([]Option, 0, len(qualityOK))
	minimumMS := qualityOK[0].EstimateMS
	for _, op := range qualityOK {
		if op.EstimateMS < minimumMS {
			minimumMS = op.EstimateMS
		}
		if op.EstimateMS <= c.BudgetMS {
			withinBudget = append(withinBudget, op)
		}
	}
	if len(withinBudget) == 0 {
		return nil, fault.New(fault.StageBudget, "processing_budget_unreachable", -1, -1,
			fmt.Errorf("quality gate %.2f requires at least %dms but budget is %dms",
				c.MinQuality, minimumMS, c.BudgetMS))
	}

	sort.SliceStable(withinBudget, func(i, j int) bool {
		a, b := withinBudget[i], withinBudget[j]
		if a.Bytes != b.Bytes {
			return a.Bytes < b.Bytes
		}
		if a.EstimateMS != b.EstimateMS {
			return a.EstimateMS < b.EstimateMS
		}
		if a.Quality != b.Quality {
			return a.Quality > b.Quality
		}
		return a.Name < b.Name
	})
	selected := withinBudget[0]
	if c.MaxBytes > 0 && selected.Bytes > c.MaxBytes {
		return nil, fault.New(fault.StageBudget, "delivery_budget_unreachable", -1, -1,
			fmt.Errorf("smallest quality-preserving profile %q is %d bytes; max-bytes=%d",
				selected.Name, selected.Bytes, c.MaxBytes))
	}

	return &Decision{
		Selected: selected, Quality: selected.Quality, EstimateMS: selected.EstimateMS,
		OutputBytes: selected.Bytes, QualityCandidates: len(qualityOK),
		BudgetCandidates: len(withinBudget),
	}, nil
}

// DefaultOptions builds deterministic bounded profiles for a workload. The
// estimates are deliberately simple and local: byte volume plus fixed per-frame
// work. They are not an encoder benchmark.
func DefaultOptions(frameCount int, pixelBytes int64) []Option {
	if frameCount < 0 {
		frameCount = 0
	}
	if pixelBytes < 0 {
		pixelBytes = 0
	}
	baseMS := int64(frameCount) + (pixelBytes+65535)/65536
	ppmBytes := pixelBytes + int64(frameCount)*32 // PPM headers and timeline labels
	return []Option{
		{Name: "rgb24-passthrough", Quality: 100, EstimateMS: baseMS, Bytes: ppmBytes},
		{Name: "checksum-dedup-segments", Quality: 99.9, EstimateMS: baseMS + int64(frameCount)/4, Bytes: ppmBytes*85/100 + 64},
		{Name: "metadata-compact", Quality: 99.5, EstimateMS: baseMS + int64(frameCount)/2, Bytes: ppmBytes*70/100 + 64},
	}
}
