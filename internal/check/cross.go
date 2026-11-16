package check

import (
	"fmt"

	"hlscheck/internal/ts"
)

// boundaryLimitMs is the tolerated cross-profile boundary deviation.
const boundaryLimitMs = 100.0

// CrossReport describes alignment defects across profiles.
type CrossReport struct {
	BoundaryDeviationMs float64  `json:"boundary_deviation_ms"`
	Details             []string `json:"details,omitempty"`
}

// CheckCross compares segment start PTS across profiles for every
// shared manifest sequence number.
func CheckCross(reps ...*ProfileReport) *CrossReport {
	cr := &CrossReport{}
	type obs struct {
		name  string
		start int64
	}
	bySeq := map[int][]obs{}
	maxSeq := -1
	for _, r := range reps {
		for seq, st := range r.Starts {
			bySeq[seq] = append(bySeq[seq], obs{r.Name, st})
			if seq > maxSeq {
				maxSeq = seq
			}
		}
	}
	for seq := 0; seq <= maxSeq; seq++ {
		list := bySeq[seq]
		if len(list) < 2 {
			continue
		}
		lo, hi := list[0].start, list[0].start
		for _, o := range list[1:] {
			lo = min(lo, o.start)
			hi = max(hi, o.start)
		}
		dev := float64(hi-lo) / (ts.ClockRate / 1000)
		if dev > cr.BoundaryDeviationMs {
			cr.BoundaryDeviationMs = dev
		}
		if dev > boundaryLimitMs {
			cr.Details = append(cr.Details,
				fmt.Sprintf("seq %d: boundary deviation %.1f ms across %d profiles", seq, dev, len(list)))
		}
	}
	return cr
}
