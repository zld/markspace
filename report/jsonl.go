// Package report renders pipeline results as newline-delimited JSON. One line
// is a run-level summary; one line per segment records the reviewable
// conclusion. Output is deterministic so two runs of the same input produce
// byte-identical JSONL.
package report

import (
	"encoding/json"
	"io"

	"mediad/analyze"
	"mediad/merge"
	"mediad/plan"
)

// Summary is the single run-level JSONL object.
type Summary struct {
	Kind           string             `json:"kind"`
	SamplesTotal   int64              `json:"samples_total"`
	DamageRanges   []analyze.DamageRange `json:"damage_ranges"`
	QualityPass    bool               `json:"quality_pass"`
	BudgetMs       uint64             `json:"budget_ms"`
	BudgetDeltaMs  int64              `json:"budget_delta_ms"`
	BytesEstimated int64              `json:"bytes_estimated"`
	BytesLimit     int64              `json:"bytes_limit"`
	Decision       string             `json:"decision"`
	Delivered      int                `json:"delivered"`
	Rejected       int                `json:"rejected"`
	Skipped        int                `json:"skipped"`
	MergeFailures  int                `json:"merge_failures"`
}

// SegmentLine is one per-segment JSONL object.
type SegmentLine struct {
	Kind        string `json:"kind"`
	InputIndex  int    `json:"input_index"`
	Name        string `json:"name"`
	SampleStart int64  `json:"sample_start"`
	SampleEnd   int64  `json:"sample_end"`
	QualityPass bool   `json:"quality_pass"`
	Decision    string `json:"decision"`
	Reason      string `json:"reason,omitempty"`
}

func writeLine(w io.Writer, v any) error {
	raw, err := json.Marshal(v)
	if err != nil {
		return err
	}
	if _, err := w.Write(append(raw, '\n')); err != nil {
		return err
	}
	return nil
}

// overallDecision rolls per-segment decisions up into one run verdict.
func overallDecision(p *plan.Plan, mergeFailures int) string {
	rejected, skipped := 0, 0
	deliverable := 0
	for _, it := range p.Items {
		switch it.Decision {
		case plan.DecisionDeliver:
			deliverable++
		case plan.DecisionRejectQuality:
			rejected++
		default:
			skipped++
		}
	}
	switch {
	case deliverable == 0:
		return "none_deliverable"
	case rejected > 0 || skipped > 0 || mergeFailures > 0:
		return "partial_delivery"
	default:
		return "deliver_all"
	}
}

// Write emits the summary line followed by one line per segment in plan
// order. An empty damage list is rendered as [] rather than null.
func Write(w io.Writer, analysis *analyze.Report, p *plan.Plan, order *merge.Order) error {
	damage := analysis.Damage
	if damage == nil {
		damage = []analyze.DamageRange{}
	}
	qualityPass := true
	rejected, skipped, delivered := 0, 0, 0
	lines := make([]SegmentLine, 0, len(p.Items))
	for _, it := range p.Items {
		line := SegmentLine{
			Kind:        "segment",
			InputIndex:  it.InputIndex,
			Name:        it.Name,
			SampleStart: it.SampleStart,
			SampleEnd:   it.SampleEnd,
			QualityPass: it.QualityPass,
			Decision:    it.Decision,
		}
		if !it.QualityPass && len(it.Reasons) > 0 {
			line.Reason = string(it.Reasons[0].Category)
		}
		switch it.Decision {
		case plan.DecisionDeliver:
			delivered++
		case plan.DecisionRejectQuality:
			rejected++
			qualityPass = false
		default:
			skipped++
		}
		lines = append(lines, line)
	}
	mergeFailures := 0
	if order != nil {
		mergeFailures = len(order.Failures)
	}

	summary := Summary{
		Kind:           "summary",
		SamplesTotal:   analysis.SamplesTotal,
		DamageRanges:   damage,
		QualityPass:    qualityPass,
		BudgetMs:       p.BudgetMs,
		BudgetDeltaMs:  p.BudgetDeltaMs,
		BytesEstimated: p.BytesEstimated,
		BytesLimit:     p.BytesLimit,
		Decision:       overallDecision(p, mergeFailures),
		Delivered:      delivered,
		Rejected:       rejected,
		Skipped:        skipped,
		MergeFailures:  mergeFailures,
	}
	if err := writeLine(w, summary); err != nil {
		return err
	}
	for _, line := range lines {
		if err := writeLine(w, line); err != nil {
			return err
		}
	}
	return nil
}
