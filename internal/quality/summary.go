package quality

import (
	"fmt"
	"math"
)

// Summary is the quantitative record printed at the end of a run.
type Summary struct {
	TargetKbps      float64 `json:"target_kbps"`
	ActualKbps      float64 `json:"actual_kbps"`
	DeviationPct    float64 `json:"deviation_pct"`
	PSNRdB          float64 `json:"psnr_db"`
	QualityMin      float64 `json:"quality_min_db"`
	BudgetMS        int64   `json:"budget_ms"`
	ProcessMS       int64   `json:"process_ms"`
	Decision        string  `json:"decision"`
	Reason          string  `json:"reason"`
	WithinTolerance bool    `json:"within_tolerance"`
}

// Summarize builds the run report from a selected candidate.
func Summarize(cfg Config, sel Candidate, reason string) Summary {
	actual := sel.ActualKbps()
	return Summary{
		TargetKbps:      cfg.TargetKbps,
		ActualKbps:      actual,
		DeviationPct:    sel.Deviation(cfg.TargetKbps) * 100,
		PSNRdB:          sel.PSNRdB,
		QualityMin:      cfg.QualityMin,
		BudgetMS:        cfg.Budget,
		ProcessMS:       sel.ProcessMS,
		Decision:        "accept",
		Reason:          reason,
		WithinTolerance: math.Abs(sel.Deviation(cfg.TargetKbps)) <= cfg.RateTolerance,
	}
}

// PSNR converts a mean squared error into dB, clamping to the usual 100 dB
// ceiling for a perfect match.
func PSNR(mse float64, peak float64) float64 {
	if mse <= 0 {
		return 100
	}
	if peak <= 0 {
		return 0
	}
	return 10 * math.Log10(peak*peak/mse)
}

func (s Summary) String() string {
	return fmt.Sprintf("target_kbps=%.0f actual_kbps=%.0f deviation=%.1f%% psnr_db=%.2f decision=%s",
		s.TargetKbps, s.ActualKbps, s.DeviationPct, s.PSNRdB, s.Decision)
}
