// Package plan turns analyzed intervals into delivery decisions. The
// priority order is fixed: quality gate first, processing budget second, and
// byte-volume control last. A lower-priority constraint can never rescue a
// segment rejected by a higher-priority one.
package plan

import (
	"mediad/analyze"
	"mediad/media"
)

// Decision tokens are stable strings used verbatim in the JSONL report.
const (
	DecisionDeliver      = "deliver"
	DecisionRejectQuality = "reject_quality"
	DecisionSkipBudget   = "skip_budget"
	DecisionSkipBytes    = "skip_bytes"
)

// Constraints bound the delivery plan. A zero value means the constraint is
// disabled (infinite headroom), which keeps demo wiring explicit.
type Constraints struct {
	BudgetMs uint64
	MaxBytes int64
}

// Item is one per-segment decision with the accounting that produced it.
type Item struct {
	InputIndex     int           `json:"input_index"`
	Name           string        `json:"name"`
	SampleStart    int64         `json:"sample_start"`
	SampleEnd      int64         `json:"sample_end"`
	QualityPass    bool          `json:"quality_pass"`
	CostMs         uint64        `json:"cost_ms"`
	BytesEstimated int64         `json:"bytes_estimated"`
	Decision       string        `json:"decision"`
	Reasons        []media.Fault `json:"reasons,omitempty"`
}

// Plan is the full decision set plus consumed/remaining headroom.
type Plan struct {
	Items         []Item `json:"items"`
	BudgetMs      uint64 `json:"budget_ms"`
	BudgetDeltaMs int64  `json:"budget_delta_ms"` // remaining budget; negative if exceeded
	BytesEstimated int64 `json:"bytes_estimated"`
	BytesLimit    int64  `json:"bytes_limit"`
	BytesDelta    int64  `json:"bytes_delta"`
}

// Decide walks intervals in their deterministic analyzed order. Quality
// failures are rejected outright. Surviving segments are admitted while both
// processing budget and byte headroom remain; the first exhausted constraint
// and every later segment are recorded as explicit skips.
func Decide(intervals []analyze.Interval, tb media.TimeBase, c Constraints) (*Plan, error) {
	p := &Plan{
		Items:      make([]Item, 0, len(intervals)),
		BudgetMs:   c.BudgetMs,
		BytesLimit: c.MaxBytes,
		BytesDelta: c.MaxBytes,
	}
	budgetExhausted := false
	bytesExhausted := false
	var budgetUsed uint64
	var bytesUsed int64

	for _, iv := range intervals {
		item := Item{
			InputIndex:     iv.InputIndex,
			Name:           iv.Name,
			SampleStart:    iv.SampleStart,
			SampleEnd:      iv.SampleEnd,
			QualityPass:    iv.QualityPass,
			BytesEstimated: iv.Bytes,
			Reasons:        iv.DamageRanges,
		}
		samples := uint64(iv.SampleEnd - iv.SampleStart)
		cost, err := tb.SamplesToMillis(samples)
		if err != nil {
			return nil, err
		}
		item.CostMs = cost

		// Priority 1: quality. Nothing downstream can override this.
		if !iv.QualityPass {
			item.Decision = DecisionRejectQuality
			p.Items = append(p.Items, item)
			continue
		}
		// Priority 2: processing budget. Once exhausted it stays exhausted.
		if c.BudgetMs > 0 && budgetExhausted {
			item.Decision = DecisionSkipBudget
			p.Items = append(p.Items, item)
			continue
		}
		if c.BudgetMs > 0 && budgetUsed+cost > c.BudgetMs {
			budgetExhausted = true
			item.Decision = DecisionSkipBudget
			p.Items = append(p.Items, item)
			continue
		}
		// Priority 3: byte volume. Only evaluated after quality and budget.
		if c.MaxBytes > 0 && bytesExhausted {
			item.Decision = DecisionSkipBytes
			p.Items = append(p.Items, item)
			continue
		}
		if c.MaxBytes > 0 && bytesUsed+iv.Bytes > c.MaxBytes {
			bytesExhausted = true
			item.Decision = DecisionSkipBytes
			p.Items = append(p.Items, item)
			continue
		}

		item.Decision = DecisionDeliver
		budgetUsed += cost
		bytesUsed += iv.Bytes
		p.Items = append(p.Items, item)
	}

	p.BudgetDeltaMs = int64(c.BudgetMs) - int64(budgetUsed)
	p.BytesEstimated = bytesUsed
	if c.MaxBytes > 0 {
		p.BytesDelta = c.MaxBytes - bytesUsed
	}
	return p, nil
}
