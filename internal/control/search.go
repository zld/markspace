package control

import (
	"fmt"
	"math"
)

// QP search bounds and policy limits.
const (
	MinQP       = 2
	MaxQP       = 31
	MaxRounds   = 5
	probeLow    = 8
	probeMid    = 16
	probeHigh   = 24
	convergePct = 8.0 // within 8% of target counts as converged
)

// Fallback reasons recorded when model-based search is abandoned.
const (
	ReasonInversion = "bitrate-relation-inverted"
	ReasonFitFailed = "fit-failed"
)

// SegmentSearch holds the per-segment QP policy state. It is pure logic:
// the caller supplies measured samples through Observe and asks for the
// next candidate QP via Next, so the state survives process restarts when
// samples are reloaded from the journal.
type SegmentSearch struct {
	TargetKbps  float64
	samples     []Sample
	tried       map[int]bool
	rounds      int
	bounded     bool
	reason      string
	loQP        int
	hiQP        int
	convergedQP int
}

// NewSegmentSearch initializes a search for the given target bitrate.
func NewSegmentSearch(targetKbps float64) *SegmentSearch {
	return &SegmentSearch{
		TargetKbps: targetKbps,
		tried:      map[int]bool{},
		loQP:       MinQP,
		hiQP:       MaxQP,
	}
}

// InitialProbes returns the three QPs used to bootstrap the model.
func InitialProbes() []int { return []int{probeLow, probeMid, probeHigh} }

func clampQP(qp int) int {
	if qp < MinQP {
		return MinQP
	}
	if qp > MaxQP {
		return MaxQP
	}
	return qp
}

// Next returns the QP to evaluate next, or -1 when the search is finished.
// The initial three probes are always emitted first (skipping any already
// observed on resume); afterwards either the fitted model or, after an
// inversion/fit failure, a bounded midpoint search drives candidates for at
// most MaxRounds update rounds.
func (s *SegmentSearch) Next() int {
	for _, qp := range InitialProbes() {
		if !s.tried[qp] {
			return qp
		}
	}
	if s.convergedQP != 0 || s.rounds >= MaxRounds {
		return -1
	}
	if s.bounded {
		return s.boundedNext()
	}
	return s.modelNext()
}

func (s *SegmentSearch) modelNext() int {
	model, err := FitBitrate(s.samples)
	if err != nil {
		s.bounded, s.reason = true, ReasonFitFailed
		if err == ErrInverted {
			s.reason = ReasonInversion
		}
		return s.boundedNext()
	}
	qp := clampQP(PredictBitrate(model, s.TargetKbps))
	for s.tried[qp] {
		qp = clampQP(qp + 1)
	}
	return qp
}

// boundedNext bisects the QP interval using measured bitrates: the lowest
// tried QP whose bitrate is above target and the highest tried QP whose
// bitrate is below target bracket the solution.
func (s *SegmentSearch) boundedNext() int {
	lo, hi := MinQP, MaxQP
	loFound, hiFound := false, false
	for _, sm := range s.samples {
		switch {
		case sm.BitrateKbps >= s.TargetKbps:
			if !loFound || sm.QP > lo {
				lo, loFound = sm.QP, true
			}
		case sm.BitrateKbps < s.TargetKbps:
			if !hiFound || sm.QP < hi {
				hi, hiFound = sm.QP, true
			}
		}
	}
	if !loFound {
		lo = MinQP
	}
	if !hiFound {
		hi = MaxQP
	}
	if hi < lo {
		lo, hi = hi, lo
	}
	qp := clampQP((lo + hi) / 2)
	for s.tried[qp] && qp < MaxQP {
		qp++
	}
	if s.tried[qp] {
		return -1
	}
	return qp
}

// Observe records a completed candidate and counts an update round once the
// bootstrap probes are in. Convergence within convergePct of target ends the
// search early.
func (s *SegmentSearch) Observe(sm Sample) {
	s.samples = append(s.samples, sm)
	s.tried[sm.QP] = true
	if len(s.samples) > len(InitialProbes()) {
		s.rounds++
	}
	errPct := 100 * (sm.BitrateKbps - s.TargetKbps) / s.TargetKbps
	if math.Abs(errPct) <= convergePct {
		s.convergedQP = sm.QP
	}
}

// IsConverged reports whether a candidate already landed inside the band.
func (s *SegmentSearch) IsConverged() bool { return s.convergedQP != 0 }

// Rounds returns update rounds consumed after the initial probes.
func (s *SegmentSearch) Rounds() int { return s.rounds }

// Bounded reports whether the policy fell back to bounded search.
func (s *SegmentSearch) Bounded() bool { return s.bounded }

// Reason explains a bounded fallback ("" while model-driven).
func (s *SegmentSearch) Reason() string { return s.reason }

// Samples returns measured candidates in observation order.
func (s *SegmentSearch) Samples() []Sample {
	out := make([]Sample, len(s.samples))
	copy(out, s.samples)
	return out
}

// Best selects the measured candidate closest to the target bitrate,
// breaking ties toward higher quality (lower QP).
func (s *SegmentSearch) Best() (Sample, error) {
	if len(s.samples) == 0 {
		return Sample{}, fmt.Errorf("search: no samples observed")
	}
	best := s.samples[0]
	bestErr := math.Abs(best.BitrateKbps-s.TargetKbps) / s.TargetKbps
	for _, sm := range s.samples[1:] {
		err := math.Abs(sm.BitrateKbps-s.TargetKbps) / s.TargetKbps
		if err < bestErr || (err == bestErr && sm.QP < best.QP) {
			best, bestErr = sm, err
		}
	}
	return best, nil
}
