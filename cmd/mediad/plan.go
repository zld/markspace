package main

import (
	"fmt"

	"mediad/internal/clock"
)

// candidate is one deliverable variant under consideration.
type candidate struct {
	Name    string
	Quality int // higher is better; gate is a minimum
	CostMs  int64
	Bytes   int64
}

// choosePlan applies the fixed tradeoff order:
//  1. quality gate — drop candidates below the gate, never degrade silently;
//  2. processing budget — drop candidates exceeding the budget;
//  3. size — among the feasible survivors prefer those within maxBytes,
//     then the smallest.
//
// With no feasible survivor the failure reason is recorded and returned.
func choosePlan(cands []candidate, qualityGate int, budgetMs, maxBytes int64) (candidate, string, error) {
	var feasible []candidate
	for _, c := range cands {
		if c.Quality >= qualityGate {
			feasible = append(feasible, c)
		}
	}
	if len(feasible) == 0 {
		reason := fmt.Sprintf("no candidate meets quality gate %d", qualityGate)
		return candidate{}, reason, clock.New(clock.ErrNoFeasiblePlan, "%s", reason)
	}
	var withinBudget []candidate
	for _, c := range feasible {
		if c.CostMs <= budgetMs {
			withinBudget = append(withinBudget, c)
		}
	}
	if len(withinBudget) == 0 {
		reason := fmt.Sprintf("quality-feasible candidates exceed budget %dms", budgetMs)
		return candidate{}, reason, clock.New(clock.ErrNoFeasiblePlan, "%s", reason)
	}
	// Size compression last: prefer within cap, then smallest bytes.
	best := withinBudget[0]
	for _, c := range withinBudget[1:] {
		bestOK := best.Bytes <= maxBytes
		cOK := c.Bytes <= maxBytes
		switch {
		case cOK && !bestOK:
			best = c
		case cOK == bestOK && c.Bytes < best.Bytes:
			best = c
		}
	}
	reason := fmt.Sprintf("gate>=%d budget<=%dms: %d feasible, chose smallest bytes",
		qualityGate, budgetMs, len(withinBudget))
	return best, reason, nil
}
