// Package plan chooses a processing action when quality thresholds, CPU/byte
// budgets and delivery-size limits conflict. The priority order is fixed:
// quality threshold first, then processing budget, then delivery size. The
// quality gate is never silently lowered.
package plan

import (
	"sort"

	"mediad/internal/model"
)

// AlgorithmVersion pins the bitrate and objective-quality algorithms so every
// report can be reproduced and compared.
const AlgorithmVersion = "mediad-plan/v1"

// Action is the chosen processing treatment for one track.
type Action string

const (
	ActionKeep      Action = "keep"      // pass through unchanged
	ActionTranscode Action = "transcode" // re-encode at candidate bitrate
	ActionTrim      Action = "trim"      // drop non-gap redundant material
	ActionReject    Action = "reject"    // no feasible candidate
)

// Candidate is one selectable encoding profile.
type Candidate struct {
	Name        string
	BitrateBPS  uint64  // target output bitrate
	Quality     float64 // objective quality, 0..1 (higher is better)
	EstCPUNanos int64   // estimated stage CPU time
	EstBytes    int64   // estimated output size
}

// Constraints expresses the three gates. Priority when they conflict:
// MinQuality, then budgets, then MaxBytes.
type Constraints struct {
	MinQuality    float64
	CPUBudgetNS   int64
	ByteBudget    int64
	MaxDeliveryB  int64
	TargetBitrate uint64
}

// Decision records the selected candidate and the reason every other profile
// was ruled out, so an infeasible plan never fails silently.
type Decision struct {
	Action    Action
	Candidate *Candidate
	Rejects   []RejectReason
	Feasible  bool
	Algorithm string
}

// RejectReason pairs a candidate with the gate that ruled it out.
type RejectReason struct {
	Candidate string
	Gate      string
	Detail    string
}

// Choose selects the feasible candidate with the smallest delivery size.
// Candidates failing MinQuality are rejected before any budget or size check,
// guaranteeing the quality threshold is never traded away.
func Choose(cands []Candidate, c Constraints) (*Decision, error) {
	d := &Decision{Algorithm: AlgorithmVersion, Action: ActionReject}
	if len(cands) == 0 {
		return nil, model.ErrPlanInfeasible.At("", 0, "plan", 0,
			"no encoding candidates supplied")
	}
	type scored struct {
		c      Candidate
		reject *RejectReason
	}
	var feasible []scored
	for _, cnd := range cands {
		// Gate 1: quality threshold (highest priority, never relaxed).
		if cnd.Quality+1e-12 < c.MinQuality {
			d.Rejects = append(d.Rejects, RejectReason{
				Candidate: cnd.Name, Gate: "quality",
				Detail: "quality below threshold",
			})
			continue
		}
		// Gate 2: processing budget (CPU time and stage byte budget).
		if c.CPUBudgetNS > 0 && cnd.EstCPUNanos > c.CPUBudgetNS {
			d.Rejects = append(d.Rejects, RejectReason{
				Candidate: cnd.Name, Gate: "cpu_budget",
				Detail: "estimated CPU exceeds stage budget",
			})
			continue
		}
		if c.ByteBudget > 0 && cnd.EstBytes > c.ByteBudget {
			d.Rejects = append(d.Rejects, RejectReason{
				Candidate: cnd.Name, Gate: "byte_budget",
				Detail: "estimated bytes exceed stage budget",
			})
			continue
		}
		// Gate 3: delivery size (lowest priority among the hard gates).
		if c.MaxDeliveryB > 0 && cnd.EstBytes > c.MaxDeliveryB {
			d.Rejects = append(d.Rejects, RejectReason{
				Candidate: cnd.Name, Gate: "delivery_size",
				Detail: "estimated output exceeds delivery cap",
			})
			continue
		}
		feasible = append(feasible, scored{c: cnd})
	}
	if len(feasible) == 0 {
		d.Feasible = false
		return d, model.ErrPlanInfeasible.At("", 0, "plan", 0,
			"no candidate satisfies quality then budget then size gates")
	}
	sort.SliceStable(feasible, func(i, j int) bool {
		if feasible[i].c.EstBytes != feasible[j].c.EstBytes {
			return feasible[i].c.EstBytes < feasible[j].c.EstBytes
		}
		return feasible[i].c.Name < feasible[j].c.Name
	})
	pick := feasible[0].c
	d.Candidate = &pick
	d.Feasible = true
	d.Action = ActionTranscode
	if c.TargetBitrate > 0 && pick.BitrateBPS == c.TargetBitrate {
		d.Action = ActionKeep
	}
	return d, nil
}

// Budget is the runtime processing budget for a stage: CPU time ceiling plus a
// byte ceiling. Exceeding either stops the stage while preserving resumable
// state.
type Budget struct {
	MaxCPUNanos int64
	MaxBytes    int64
}

// Guard tracks live consumption against a Budget.
type Guard struct {
	Budget    Budget
	usedCPU   int64
	usedBytes int64
}

// NewGuard builds a budget guard.
func NewGuard(b Budget) *Guard { return &Guard{Budget: b} }

// Add accounts for more CPU time and bytes.
func (g *Guard) Add(cpuNanos int64, bytes int64) {
	g.usedCPU += cpuNanos
	g.usedBytes += bytes
}

// UsedCPU and UsedBytes expose consumption for manifests.
func (g *Guard) UsedCPU() int64   { return g.usedCPU }
func (g *Guard) UsedBytes() int64 { return g.usedBytes }

// Exceeded reports which budget (if any) has been exhausted.
func (g *Guard) Exceeded() (cpu bool, bytes bool) {
	cpu = g.Budget.MaxCPUNanos > 0 && g.usedCPU > g.Budget.MaxCPUNanos
	bytes = g.Budget.MaxBytes > 0 && g.usedBytes > g.Budget.MaxBytes
	return
}

// Check returns a located ERR_BUDGET_EXCEEDED once a ceiling is crossed.
func (g *Guard) Check(stage string) error {
	cpu, by := g.Exceeded()
	if cpu {
		return model.ErrBudgetExceeded.At("", 0, stage, 0, "stage CPU budget exhausted")
	}
	if by {
		return model.ErrBudgetExceeded.At("", 0, stage, 0, "stage byte budget exhausted")
	}
	return nil
}
