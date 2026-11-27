// Package quality measures objective quality and bitrate and selects a
// candidate under a fixed priority: quality gate first, processing budget
// second, then the smallest output among the survivors.
package quality

import (
	"math"

	"mediacore/internal/fault"
)

// Config is the decision policy.
type Config struct {
	TargetKbps    float64 `json:"target_kbps"`
	QualityMin    float64 `json:"quality_min_db"`
	Budget        int64   `json:"budget_ms"`
	RateTolerance float64 `json:"rate_tolerance"`
}

// DefaultRateTolerance is the documented 10% bitrate deviation allowance.
const DefaultRateTolerance = 0.10

// Validate checks the policy for impossible combinations.
func (c Config) Validate() error {
	if c.TargetKbps <= 0 {
		return fault.Newf(fault.CodeBudgetExceeded, "", "target bitrate must be positive, got %g", c.TargetKbps)
	}
	if c.QualityMin < 0 {
		return fault.Newf(fault.CodeQualityBelowMin, "", "quality floor must be non-negative, got %g", c.QualityMin)
	}
	if c.Budget <= 0 {
		return fault.Newf(fault.CodeBudgetExceeded, "", "processing budget must be positive, got %d ms", c.Budget)
	}
	if c.RateTolerance <= 0 {
		return fault.Newf(fault.CodeRateDeviation, "", "rate tolerance must be positive")
	}
	return nil
}

// Candidate is one encoded rendition under consideration.
type Candidate struct {
	Name        string  `json:"name"`
	Codec       string  `json:"codec"`
	SizeBytes   int64   `json:"size_bytes"`
	DurationSec float64 `json:"duration_sec"`
	PSNRdB      float64 `json:"psnr_db"`
	ProcessMS   int64   `json:"process_ms"`
	// RateKbps is derived from SizeBytes and DurationSec when zero.
	RateKbps float64 `json:"rate_kbps"`
}

// ActualKbps returns the measured bitrate.
func (c Candidate) ActualKbps() float64 {
	if c.RateKbps > 0 {
		return c.RateKbps
	}
	if c.DurationSec <= 0 {
		return 0
	}
	return float64(c.SizeBytes) * 8 / c.DurationSec / 1000
}

// Deviation returns the signed relative deviation from the target.
func (c Candidate) Deviation(targetKbps float64) float64 {
	if targetKbps <= 0 {
		return math.Inf(1)
	}
	return (c.ActualKbps() - targetKbps) / targetKbps
}

// Verdict explains a candidate's fate. Reasons are stable strings used in
// reports and tests.
type Verdict struct {
	Candidate Candidate `json:"candidate"`
	QualityOK bool      `json:"quality_ok"`
	BudgetOK  bool      `json:"budget_ok"`
	RateOK    bool      `json:"rate_ok"`
	Selected  bool      `json:"selected"`
	Reason    string    `json:"reason"`
}
