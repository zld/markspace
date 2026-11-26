// Package plan derives a concrete transcoding configuration from quality,
// bitrate and processing-budget constraints. Every decision carries an
// explicit rationale so recovery runs can verify why a candidate won or was
// eliminated.
package plan

import (
	"fmt"
	"sort"

	"mediad/internal/media"
)

// Candidate is one selectable output configuration. Quality is the
// normalized [0,1] estimated perceptual quality; Bitrate is bits per second
// and CostPerNs is processing-budget units consumed per nanosecond of media.
type Candidate struct {
	Width     int
	Height    int
	Bitrate   int64
	Quality   Rational
	CostPerNs int64
}

// Rational mirrors media.Rational locally to keep plan self-contained while
// staying exact; values are small and compared without floating point.
type Rational struct {
	Num int64
	Den int64
}

func (r Rational) value() float64 { return float64(r.Num) / float64(r.Den) }

// Constraints bound the selection. MinQuality is inclusive, MaxBitrate and
// BudgetUnits are inclusive.
type Constraints struct {
	MinQuality  Rational
	MaxBitrate  int64
	BudgetUnits int64
}

// Rejection records why one candidate could not be selected.
type Rejection struct {
	Width  int
	Height int
	Reason string
}

// Decision is the fully explained output of Derive.
type Decision struct {
	Width        int
	Height       int
	Bitrate      int64
	Quality      Rational
	BudgetUsed   int64
	BudgetTotal  int64
	Coverage     Rational // fraction of the timeline the budget covers
	Rejections   []Rejection
	FeasibleKeys []string // candidates passing every constraint, best first
}

// NoFeasibleCandidate is returned when every candidate violates at least one
// constraint. It carries every rejection so callers can report all causes.
type NoFeasibleCandidate struct {
	Rejections []Rejection
}

func (e *NoFeasibleCandidate) Error() string {
	return "plan: no candidate satisfies quality, bitrate and budget constraints"
}

// Derive selects the highest-quality candidate that satisfies the quality
// floor and bitrate cap. The processing budget is not a hard filter: it
// drives the reported Coverage fraction so callers can see how much of the
// timeline the budget actually purchases. Ties break on smaller bitrate then
// smaller height, making the choice deterministic.
func Derive(cands []Candidate, cons Constraints, timelineDurationNs int64) (*Decision, error) {
	if timelineDurationNs <= 0 {
		return nil, media.NewError(media.StagePlan, "derive", "timeline duration must be positive")
	}
	dec := &Decision{BudgetTotal: cons.BudgetUnits}
	type scored struct {
		cand Candidate
		cost int64
	}
	var feasible []scored
	for _, c := range cands {
		if reason, ok := reject(c, cons); !ok {
			cost, err := mulCost(c.CostPerNs, timelineDurationNs)
			if err != nil {
				return nil, media.NewError(media.StagePlan, "derive.budget", err.Error())
			}
			feasible = append(feasible, scored{cand: c, cost: cost})
		} else {
			dec.Rejections = append(dec.Rejections, Rejection{
				Width: c.Width, Height: c.Height, Reason: reason,
			})
		}
	}
	if len(feasible) == 0 {
		return nil, &NoFeasibleCandidate{Rejections: dec.Rejections}
	}
	sort.SliceStable(feasible, func(i, j int) bool {
		qi := feasible[i].cand.Quality
		qj := feasible[j].cand.Quality
		if cmp := cmpRat(qi, qj); cmp != 0 {
			return cmp > 0
		}
		if feasible[i].cand.Bitrate != feasible[j].cand.Bitrate {
			return feasible[i].cand.Bitrate < feasible[j].cand.Bitrate
		}
		return feasible[i].cand.Height < feasible[j].cand.Height
	})
	for _, s := range feasible {
		dec.FeasibleKeys = append(dec.FeasibleKeys,
			fmt.Sprintf("%dx%d", s.cand.Width, s.cand.Height))
	}
	win := feasible[0]
	dec.Width = win.cand.Width
	dec.Height = win.cand.Height
	dec.Bitrate = win.cand.Bitrate
	dec.Quality = win.cand.Quality
	dec.BudgetUsed = win.cost
	// Coverage = min(1, budget/cost); once the full timeline fits it is 1/1.
	if cons.BudgetUnits >= win.cost {
		dec.Coverage = Rational{Num: 1, Den: 1}
	} else {
		dec.Coverage = Rational{Num: cons.BudgetUnits, Den: win.cost}
	}
	return dec, nil
}

// reject returns a human-readable reason and true when the candidate fails
// a hard constraint (bitrate cap or quality floor). Budget is intentionally
// not a hard filter; it is expressed through Decision.Coverage instead.
func reject(c Candidate, cons Constraints) (string, bool) {
	if c.Bitrate > cons.MaxBitrate {
		return fmt.Sprintf("bitrate %d exceeds cap %d", c.Bitrate, cons.MaxBitrate), true
	}
	if cmpRat(c.Quality, cons.MinQuality) < 0 {
		return fmt.Sprintf("quality %s below floor %s", ratStr(c.Quality), ratStr(cons.MinQuality)), true
	}
	return "", false
}

// mulCost converts a per-nanosecond processing rate and a nanosecond
// duration into whole processing-budget units (one unit = one second of
// work), truncating any sub-unit remainder. Keeping the budget on a
// second-scale unit lets callers pass compact integers such as 900000000.
func mulCost(costPerNs, durNs int64) (int64, error) {
	hi, lo := mul64(costPerNs, durNs)
	if hi != 0 {
		return 0, fmt.Errorf("cost product overflows int64")
	}
	return int64(lo) / media.NanoPerSecond, nil
}

func cmpRat(a, b Rational) int {
	// a.Num/a.Den vs b.Num/b.Den using cross multiplication; dens positive.
	l := a.Num * b.Den
	r := b.Num * a.Den
	if l < r {
		return -1
	}
	if l > r {
		return 1
	}
	return 0
}

func ratStr(r Rational) string { return fmt.Sprintf("%d/%d", r.Num, r.Den) }

// QualityFloat renders the exact quality for terminal output.
func QualityFloat(r Rational) float64 { return r.value() }
