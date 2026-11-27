// Package transform derives a conversion configuration from measured
// quality, bitrate and budget figures. Every constraint reports its
// measured value, threshold, deviation and pass state; an infeasible
// budget yields an explicit infeasible conclusion, never a silently
// degraded plan.
package transform

import (
	"fmt"

	"rtx/internal/model"
)

// Constraint is one checked threshold with its evidence.
type Constraint struct {
	Name      string  `json:"name"`
	Measured  float64 `json:"measured"`
	Threshold float64 `json:"threshold"`
	Deviation float64 `json:"deviation"` // measured - threshold, signed
	Pass      bool    `json:"pass"`
}

// Input carries every measured figure the configuration depends on.
type Input struct {
	MinQuality          float64 // minimum acceptable quality score
	MeasuredQuality     float64
	TargetBitrateBPS    int64 // bitrate the transform aims at
	EstimatedBitrateBPS int64 // bitrate the input actually implies
	MaxBitrateBPS       int64 // hard ceiling for the estimate
	BudgetMS            int64 // processing budget
	EstimatedCostMS     float64
	DriftPPM            float64
	MaxDriftPPM         float64
}

// Config is the generated conversion plan.
type Config struct {
	TargetBitrateBPS    int64        `json:"target_bitrate_bps"`
	EstimatedBitrateBPS int64        `json:"estimated_bitrate_bps"`
	BudgetMS            int64        `json:"budget_ms"`
	BudgetActualMS      float64      `json:"budget_ms_actual"`
	Feasible            bool         `json:"feasible"`
	Constraints         []Constraint `json:"constraints"`
	Anomalies           []model.Anomaly
}

// Generate evaluates every constraint and returns the plan. Feasible is
// false when any constraint fails; callers map a budget failure to the
// dedicated exit code.
func Generate(in Input) *Config {
	c := &Config{
		TargetBitrateBPS:    in.TargetBitrateBPS,
		EstimatedBitrateBPS: in.EstimatedBitrateBPS,
		BudgetMS:            in.BudgetMS,
		BudgetActualMS:      in.EstimatedCostMS,
	}
	atLeast := func(name string, measured, threshold float64) {
		c.Constraints = append(c.Constraints, Constraint{
			Name: name, Measured: measured, Threshold: threshold,
			Deviation: measured - threshold, Pass: measured >= threshold,
		})
	}
	atMost := func(name string, measured, threshold float64) {
		c.Constraints = append(c.Constraints, Constraint{
			Name: name, Measured: measured, Threshold: threshold,
			Deviation: measured - threshold, Pass: measured <= threshold,
		})
	}
	atLeast("quality", in.MeasuredQuality, in.MinQuality)
	atMost("bitrate_bps", float64(in.EstimatedBitrateBPS), float64(in.MaxBitrateBPS))
	atMost("budget_ms", in.EstimatedCostMS, float64(in.BudgetMS))
	atMost("drift_ppm", abs(in.DriftPPM), in.MaxDriftPPM)
	c.Feasible = true
	for _, con := range c.Constraints {
		if con.Pass {
			continue
		}
		c.Feasible = false
		recovery := "adjust thresholds or input"
		if con.Name == "budget_ms" {
			recovery = "increase --budget-ms or reduce input scope"
		}
		c.Anomalies = append(c.Anomalies, model.Anomaly{
			Stage: model.StageTransform, Reason: model.ReasonConstraint,
			Offset: -1, RawValue: int64(con.Measured),
			Message: fmt.Sprintf("constraint %s failed: measured %g vs threshold %g",
				con.Name, con.Measured, con.Threshold),
			Recovery: recovery,
		})
	}
	return c
}

// BudgetExceeded reports whether the budget constraint is the (or a)
// failing one; the CLI maps this to exit code 2.
func (c *Config) BudgetExceeded() bool {
	for _, con := range c.Constraints {
		if con.Name == "budget_ms" && !con.Pass {
			return true
		}
	}
	return false
}

func abs(f float64) float64 {
	if f < 0 {
		return -f
	}
	return f
}
