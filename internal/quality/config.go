package quality

import (
	"fmt"
	"strings"

	"pwf/internal/media"
)

// Decision is the per-track conversion action selected by the planner.
type Decision string

const (
	// DecisionPassthrough means the source needs no conversion.
	DecisionPassthrough Decision = "passthrough"
	// DecisionTranscode means the track must be converted before handoff.
	DecisionTranscode Decision = "transcode"
	// DecisionReject means the track fails a hard quality gate.
	DecisionReject Decision = "reject"
)

// Thresholds are the explicit, quantifiable gates of one planning run.
type Thresholds struct {
	MaxGapPackets      uint64   // hard gate: reject above this
	MaxAnomalyRatio    Rational // hard gate: reject above this
	TargetBitrateBps   int64    // soft gate: transcode when exceeded
	BudgetTotalCents   int64    // total processing budget, integer cents
	TranscodeCostCents int64    // cost charged per transcoded track
}

// DefaultThresholds are the gates used by the verify command.
func DefaultThresholds() Thresholds {
	return Thresholds{
		MaxGapPackets:      3,
		MaxAnomalyRatio:    Rational{Num: 5, Den: 100}, // 5%
		TargetBitrateBps:   160_000,
		BudgetTotalCents:   1000,
		TranscodeCostCents: 300,
	}
}

// TightBudget returns thresholds whose budget cannot fit even one
// transcode, exercising the infeasible-budget path.
func TightBudget() Thresholds {
	t := DefaultThresholds()
	t.BudgetTotalCents = t.TranscodeCostCents - 1
	return t
}

// TrackConfig is the deliverable per-track conversion configuration.
type TrackConfig struct {
	Track            string
	Decision         Decision
	MeasuredMaxGap   uint64
	MeasuredAnomaly  Rational
	MeasuredBitrate  int64
	MeasuredPackets  int
	Reasons          []string
	UnmetConstraints []string
	CostCents        int64
}

// Plan is the full planning result for a set of tracks.
type Plan struct {
	Tracks           []TrackConfig
	TotalCostCents   int64
	BudgetCents      int64
	Infeasible       bool
	InfeasibleReason string
}

// PlanTracks applies the quantifiable gates and charges the processing
// budget. A hard gate violation rejects the track; exceeding the bitrate
// target selects transcode; otherwise the track passes through. If the
// summed cost exceeds the budget, planning fails rather than silently
// dropping work.
func PlanTracks(measurements []*Measurement, th Thresholds) (*Plan, error) {
	plan := &Plan{BudgetCents: th.BudgetTotalCents}
	for _, m := range measurements {
		cfg := TrackConfig{
			Track:           m.Track,
			MeasuredMaxGap:  m.MaxGap,
			MeasuredAnomaly: m.AnomalyRatio,
			MeasuredBitrate: m.AvgBitrateBps,
			MeasuredPackets: m.PacketCount,
		}
		switch {
		case m.MaxGap > th.MaxGapPackets:
			cfg.Decision = DecisionReject
			cfg.Reasons = append(cfg.Reasons, fmt.Sprintf(
				"max gap %d packets exceeds gate %d", m.MaxGap, th.MaxGapPackets))
			cfg.UnmetConstraints = append(cfg.UnmetConstraints, "max_gap_packets")
		case !m.AnomalyRatio.LessThan(th.MaxAnomalyRatio) && m.AnomalyRatio.Den != 0:
			cfg.Decision = DecisionReject
			cfg.Reasons = append(cfg.Reasons, fmt.Sprintf(
				"anomaly ratio %s exceeds gate %s", m.AnomalyRatio, th.MaxAnomalyRatio))
			cfg.UnmetConstraints = append(cfg.UnmetConstraints, "anomaly_ratio")
		case m.AvgBitrateBps > th.TargetBitrateBps:
			cfg.Decision = DecisionTranscode
			cfg.CostCents = th.TranscodeCostCents
			cfg.Reasons = append(cfg.Reasons, fmt.Sprintf(
				"bitrate %d bps exceeds target %d bps", m.AvgBitrateBps, th.TargetBitrateBps))
			cfg.UnmetConstraints = append(cfg.UnmetConstraints, "target_bitrate_bps")
		default:
			cfg.Decision = DecisionPassthrough
			cfg.Reasons = append(cfg.Reasons, "all gates satisfied; no conversion required")
		}
		plan.Tracks = append(plan.Tracks, cfg)
		plan.TotalCostCents += cfg.CostCents
	}
	if plan.TotalCostCents > th.BudgetTotalCents {
		plan.Infeasible = true
		plan.InfeasibleReason = fmt.Sprintf(
			"required cost %d cents exceeds budget %d cents", plan.TotalCostCents, th.BudgetTotalCents)
		return plan, media.NewError(media.KindPlanInfeasible, media.StagePlan,
			plan.InfeasibleReason, nil)
	}
	return plan, nil
}

// Summary renders a deterministic one-line-per-track report.
func (p *Plan) Summary() string {
	var b strings.Builder
	for _, c := range p.Tracks {
		b.WriteString(fmt.Sprintf("track=%s decision=%s cost_cents=%d reasons=%q\n",
			c.Track, c.Decision, c.CostCents, strings.Join(c.Reasons, "; ")))
	}
	fmt.Fprintf(&b, "total_cost_cents=%d budget_cents=%d\n", p.TotalCostCents, p.BudgetCents)
	return b.String()
}
