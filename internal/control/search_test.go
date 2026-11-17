package control

import (
	"math"
	"testing"
)

// runSearch drives a SegmentSearch with a synthetic bitrate function.
func runSearch(s *SegmentSearch, bitrate func(qp int) float64) {
	for {
		qp := s.Next()
		if qp < 0 {
			return
		}
		s.Observe(Sample{QP: qp, BitrateKbps: bitrate(qp), PSNRdB: float64(50 - qp)})
	}
}

func TestSearchModelDrivenConvergence(t *testing.T) {
	// Exponential model: target 600 kbps is reached near QP 15.
	bitrate := func(qp int) float64 { return 1000 * math.Exp(-0.05*float64(qp)) }
	s := NewSegmentSearch(600)
	runSearch(s, bitrate)
	best, err := s.Best()
	if err != nil {
		t.Fatal(err)
	}
	errPct := 100 * (best.BitrateKbps - 600) / 600
	if absF(errPct) > 15 {
		t.Fatalf("best error %f%% too large (qp=%d b=%.1f)", errPct, best.QP, best.BitrateKbps)
	}
	if s.Bounded() {
		t.Fatal("should stay model-driven")
	}
	if s.Rounds() > MaxRounds {
		t.Fatal("round budget exceeded")
	}
	// No duplicate QP observations.
	seen := map[int]bool{}
	for _, sm := range s.Samples() {
		if seen[sm.QP] {
			t.Fatalf("duplicate QP %d", sm.QP)
		}
		seen[sm.QP] = true
	}
}

func TestSearchInversionFallback(t *testing.T) {
	// Inverted: bitrate grows with QP.
	bitrate := func(qp int) float64 { return 20 * float64(qp*qp) }
	s := NewSegmentSearch(3000)
	runSearch(s, bitrate)
	if !s.Bounded() || s.Reason() != ReasonInversion {
		t.Fatalf("want bounded inversion fallback, got bounded=%v reason=%q", s.Bounded(), s.Reason())
	}
	if _, err := s.Best(); err != nil {
		t.Fatal(err)
	}
	if s.Rounds() > MaxRounds {
		t.Fatal("round budget exceeded")
	}
}

func TestSearchRoundCap(t *testing.T) {
	// Flat bitrate: never converges, exercises the five-round cap.
	s := NewSegmentSearch(99999)
	runSearch(s, func(int) float64 { return 500 })
	if s.Rounds() > MaxRounds {
		t.Fatalf("rounds %d exceed cap", s.Rounds())
	}
	if len(s.Samples()) > len(InitialProbes())+MaxRounds {
		t.Fatalf("too many candidates: %d", len(s.Samples()))
	}
}

func TestSearchResumeSkipsObserved(t *testing.T) {
	s := NewSegmentSearch(600)
	bitrate := func(qp int) float64 { return 1000 * math.Exp(-0.05*float64(qp)) }
	// Simulate a crashed run after the three bootstrap probes.
	for _, qp := range InitialProbes() {
		s.Observe(Sample{QP: qp, BitrateKbps: bitrate(qp), PSNRdB: float64(50 - qp)})
	}
	runSearch(s, bitrate)
	if _, err := s.Best(); err != nil {
		t.Fatal(err)
	}
	for _, sm := range s.Samples() {
		if sm.QP < MinQP || sm.QP > MaxQP {
			t.Fatalf("QP out of range: %d", sm.QP)
		}
	}
}

func TestBestEmpty(t *testing.T) {
	if _, err := NewSegmentSearch(100).Best(); err == nil {
		t.Fatal("empty search must error")
	}
}

func absF(x float64) float64 {
	if x < 0 {
		return -x
	}
	return x
}
