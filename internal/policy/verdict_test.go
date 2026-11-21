package policy

import (
	"math"
	"testing"

	"loudcheck/internal/loudness"
)

func measured(lufs, truePeakDB float64) loudness.Result {
	return loudness.Result{
		IntegratedLUFS:  lufs,
		IntegratedValid: true,
		TruePeak:        math.Pow(10, truePeakDB/20),
	}
}

func TestEvaluateWithinTolerance(t *testing.T) {
	th := Thresholds{TargetLUFS: -23, ToleranceLU: 1, MaxTruePeakDBTP: -1}
	v := Evaluate(measured(-23.4, -3), th)
	if !v.Pass || !v.IntegratedPass || !v.TruePeakPass || len(v.Failures) != 0 {
		t.Fatalf("expected a clean pass, got %+v", v)
	}
}

func TestEvaluateToleranceEdges(t *testing.T) {
	th := Thresholds{TargetLUFS: -23, ToleranceLU: 1, MaxTruePeakDBTP: -1}
	for _, tc := range []struct {
		lufs float64
		pass bool
	}{
		{-24.0, true},
		{-22.0, true},
		{-24.001, false},
		{-21.999, false},
	} {
		v := Evaluate(measured(tc.lufs, -3), th)
		if v.IntegratedPass != tc.pass {
			t.Errorf("%.3f LUFS: integrated_pass = %v, want %v", tc.lufs, v.IntegratedPass, tc.pass)
		}
	}
}

func TestEvaluateTruePeakEdge(t *testing.T) {
	th := Thresholds{TargetLUFS: -23, ToleranceLU: 1, MaxTruePeakDBTP: -1}
	if v := Evaluate(measured(-23, -1.0), th); !v.TruePeakPass {
		t.Error("a true peak exactly at the limit should pass")
	}
	if v := Evaluate(measured(-23, -0.99), th); v.TruePeakPass {
		t.Error("a true peak above the limit must fail")
	}
}

func TestEvaluateKeepsBothFailures(t *testing.T) {
	th := Thresholds{TargetLUFS: -23, ToleranceLU: 1, MaxTruePeakDBTP: -1}
	v := Evaluate(measured(-15, 0.5), th)
	if v.Pass || v.IntegratedPass || v.TruePeakPass {
		t.Fatalf("expected both checks to fail: %+v", v)
	}
	if len(v.Failures) != 2 {
		t.Fatalf("failures = %v, want one per criterion", v.Failures)
	}
}

func TestUnmeasuredIsNeverPass(t *testing.T) {
	th := DefaultThresholds()
	v := Evaluate(loudness.Result{}, th)
	if v.Pass || v.IntegratedPass || v.Measured {
		t.Fatalf("an unmeasured file must not pass: %+v", v)
	}
	if len(v.Failures) == 0 {
		t.Fatal("an unmeasured file should explain why")
	}
}

func TestValidateThresholds(t *testing.T) {
	good := DefaultThresholds()
	if err := good.Validate(); err != nil {
		t.Fatalf("defaults rejected: %v", err)
	}
	for name, th := range map[string]Thresholds{
		"positive target":    {TargetLUFS: 3, ToleranceLU: 1, MaxTruePeakDBTP: -1},
		"negative tolerance": {TargetLUFS: -23, ToleranceLU: -1, MaxTruePeakDBTP: -1},
		"nan target":         {TargetLUFS: math.NaN(), ToleranceLU: 1, MaxTruePeakDBTP: -1},
		"absurd peak":        {TargetLUFS: -23, ToleranceLU: 1, MaxTruePeakDBTP: 40},
	} {
		if err := th.Validate(); err == nil {
			t.Errorf("%s: expected an error", name)
		}
	}
}
