package quality

import (
	"math"
	"testing"
)

func TestCandidateRates(t *testing.T) {
	c := Candidate{SizeBytes: 1_000_000, DurationSec: 10}
	if got := c.ActualKbps(); got != 800 {
		t.Fatalf("actual = %g", got)
	}
	if got := c.Deviation(1000); math.Abs(got-(-0.2)) > 1e-9 {
		t.Fatalf("deviation = %g", got)
	}
	explicit := Candidate{RateKbps: 500, SizeBytes: 1 << 30, DurationSec: 1}
	if explicit.ActualKbps() != 500 {
		t.Fatal("explicit rate must win")
	}
	if !math.IsInf(Candidate{}.Deviation(0), 1) {
		t.Fatal("zero target must be infinite deviation")
	}
}

func TestConfigValidation(t *testing.T) {
	bad := []Config{
		{TargetKbps: 0, QualityMin: 1, Budget: 1, RateTolerance: 0.1},
		{TargetKbps: 1, QualityMin: -1, Budget: 1, RateTolerance: 0.1},
		{TargetKbps: 1, QualityMin: 1, Budget: 0, RateTolerance: 0.1},
		{TargetKbps: 1, QualityMin: 1, Budget: 1, RateTolerance: 0},
	}
	for i, c := range bad {
		if c.Validate() == nil {
			t.Errorf("case %d should be invalid", i)
		}
	}
	good := Config{TargetKbps: 900, QualityMin: 30, Budget: 5000, RateTolerance: DefaultRateTolerance}
	if err := good.Validate(); err != nil {
		t.Fatalf("good config rejected: %v", err)
	}
}
