// Package budget decides whether a window of media work may run under the
// current link conditions.  A planner estimates the byte budget, the number of
// worker slots and the timeline window from the observed bandwidth, the number
// of transport gaps and the work still outstanding.
//
// The planner never silently clamps an over-limit request: when the estimated
// work cannot fit inside the configured caps it returns a rejected plan with a
// stable reason and a BUDGET_EXCEEDED or LIMIT_EXCEEDED fault.
package budget

import (
	"errors"
	"fmt"

	"mediapipeline/internal/clock"
	"mediapipeline/internal/fault"
)

// Limits are the hard caps a plan must respect.
type Limits struct {
	MinWindowMS     int   // smallest candidate window
	MaxWindowMS     int   // largest live window; work needing longer is rejected
	MinConcurrency  int   // at least this many workers when work exists
	MaxConcurrency  int   // never more workers than this
	MaxBytesPerTask int64 // per-task byte ceiling
	MaxBudgetBytes  int64 // per-window byte ceiling
	MaxUnits        int   // outstanding unit ceiling
}

// DefaultLimits are bounded but generous for the generated fixtures.
var DefaultLimits = Limits{
	MinWindowMS:     20,
	MaxWindowMS:     5_000,
	MinConcurrency:  1,
	MaxConcurrency:  8,
	MaxBytesPerTask: 2 << 20,
	MaxBudgetBytes:  64 << 20,
	MaxUnits:        100_000,
}

// gapPenaltyPct is how much throughput each outstanding gap costs, capped so a
// heavily damaged link is still estimated rather than assumed zero.
const gapPenaltyPct = 5

// maxGapPenaltyPct bounds the loss modelled for a bursty, damaged link.
const maxGapPenaltyPct = 50

// Input describes the state a plan is built from.
type Input struct {
	BandwidthBps   int64 // currently available link bandwidth, bytes/second
	RemainingBytes int64 // bytes of media still to process
	RemainingUnits int   // elementary units still to process
	Gaps           int   // continuous transport gaps awaiting repair
}

// Plan is the planner's decision.  A rejected plan still carries the estimates
// that led to the decision so the caller can explain it.
type Plan struct {
	WindowMS     int   // candidate timeline window in milliseconds
	Concurrency  int   // worker slots to run
	ByteBudget   int64 // bytes admitted for this window
	EstBytes     int64 // bytes the window is expected to carry
	Tasks        int   // number of per-task chunks the work splits into
	EffectiveBps int64 // bandwidth after the gap penalty
	Rejected     bool
	Reason       string
	Faults       []fault.Fault
}

// Planner applies a fixed set of limits.
type Planner struct {
	lim Limits
}

// NewPlanner validates the limits and builds a planner.
func NewPlanner(lim Limits) (*Planner, error) {
	switch {
	case lim.MinWindowMS <= 0:
		return nil, errors.New("budget: MinWindowMS must be positive")
	case lim.MaxWindowMS < lim.MinWindowMS:
		return nil, errors.New("budget: MaxWindowMS below MinWindowMS")
	case lim.MinConcurrency <= 0:
		return nil, errors.New("budget: MinConcurrency must be positive")
	case lim.MaxConcurrency < lim.MinConcurrency:
		return nil, errors.New("budget: MaxConcurrency below MinConcurrency")
	case lim.MaxBytesPerTask <= 0:
		return nil, errors.New("budget: MaxBytesPerTask must be positive")
	case lim.MaxBudgetBytes <= 0:
		return nil, errors.New("budget: MaxBudgetBytes must be positive")
	case lim.MaxUnits <= 0:
		return nil, errors.New("budget: MaxUnits must be positive")
	}
	return &Planner{lim: lim}, nil
}

// reject finalises an unsuccessful plan with one fault.
func (p *Planner) reject(plan Plan, code fault.Code, format string, args ...interface{}) Plan {
	msg := fmt.Sprintf(format, args...)
	f := fault.New(code, msg, -1, clock.MustRational(0, 1), clock.MustRational(0, 1))
	plan.Rejected = true
	plan.Reason = msg
	plan.Faults = append(plan.Faults, f)
	return plan
}

// Plan builds a plan for the given state.
//
// Order of checks is deliberate: impossible input first, then the unit cap,
// then the live-window time budget, then the byte budget.  The first failure
// decides the reported reason so output is deterministic.
func (p *Planner) Plan(in Input) Plan {
	var plan Plan
	if in.BandwidthBps <= 0 {
		return p.reject(plan, fault.CodeBudgetExceeded,
			"bandwidth %d B/s is not positive; cannot plan a window", in.BandwidthBps)
	}
	if in.RemainingBytes < 0 || in.RemainingUnits < 0 || in.Gaps < 0 {
		return p.reject(plan, fault.CodeLimitExceeded,
			"negative work counters: bytes=%d units=%d gaps=%d",
			in.RemainingBytes, in.RemainingUnits, in.Gaps)
	}
	if in.RemainingUnits > p.lim.MaxUnits {
		return p.reject(plan, fault.CodeLimitExceeded,
			"outstanding units %d exceed limit %d", in.RemainingUnits, p.lim.MaxUnits)
	}

	// Gaps cost usable throughput: each gap is modelled as a fixed percentage
	// of the link until it is repaired.  This is an estimate, not a promise.
	penalty := in.Gaps * gapPenaltyPct
	if penalty > maxGapPenaltyPct {
		penalty = maxGapPenaltyPct
	}
	effBps := in.BandwidthBps * int64(100-penalty) / 100
	if effBps <= 0 {
		effBps = 1
	}
	plan.EffectiveBps = effBps

	// Time needed to move the outstanding bytes at the effective rate.
	needMS := 0
	if in.RemainingBytes > 0 {
		needMS = int(ceilDiv(in.RemainingBytes*1000, effBps))
	}
	if needMS > p.lim.MaxWindowMS {
		return p.reject(plan, fault.CodeBudgetExceeded,
			"bandwidth %d B/s with %d gaps needs %d ms to move %d bytes, over the %d ms live-window cap",
			in.BandwidthBps, in.Gaps, needMS, in.RemainingBytes, p.lim.MaxWindowMS)
	}

	plan.WindowMS = clampInt(needMS, p.lim.MinWindowMS, p.lim.MaxWindowMS)
	windowBytes := effBps * int64(plan.WindowMS) / 1000
	if in.RemainingBytes > 0 && windowBytes <= 0 {
		windowBytes = 1
	}
	plan.EstBytes = min64(windowBytes, in.RemainingBytes)
	if plan.EstBytes > p.lim.MaxBudgetBytes {
		return p.reject(plan, fault.CodeBudgetExceeded,
			"estimated %d bytes for the window exceeds the %d byte budget",
			plan.EstBytes, p.lim.MaxBudgetBytes)
	}
	plan.ByteBudget = plan.EstBytes

	if in.RemainingBytes > 0 {
		plan.Tasks = int(ceilDiv(in.RemainingBytes, p.lim.MaxBytesPerTask))
		if plan.Tasks < 1 {
			plan.Tasks = 1
		}
	}
	plan.Concurrency = clampInt(plan.Tasks, p.lim.MinConcurrency, p.lim.MaxConcurrency)
	if in.RemainingBytes == 0 {
		plan.Concurrency = 0
	}
	return plan
}

// EstimateTasks returns the number of chunks remainingBytes splits into under
// the per-task ceiling.  The worker pool and the planner share this rule so
// their task counts always agree.
func EstimateTasks(remainingBytes, perTask int64) int {
	if remainingBytes <= 0 || perTask <= 0 {
		return 0
	}
	return int(ceilDiv(remainingBytes, perTask))
}

func ceilDiv(a, b int64) int64 {
	if b <= 0 {
		return 0
	}
	q := a / b
	if a%b != 0 {
		q++
	}
	return q
}

func clampInt(v, lo, hi int) int {
	if v < lo {
		return lo
	}
	if v > hi {
		return hi
	}
	return v
}

func min64(a, b int64) int64 {
	if a < b {
		return a
	}
	return b
}
