package validate

import (
	"strings"
	"testing"

	"dashgen/internal/align"
	"dashgen/internal/demo"
	"dashgen/internal/timebase"
)

func ms(t *testing.T, s string) timebase.Rational {
	t.Helper()
	r, err := timebase.ParseDuration(s)
	if err != nil {
		t.Fatal(err)
	}
	return r
}

func demoReport(t *testing.T) *align.Report {
	t.Helper()
	report, err := align.Build(demo.Build())
	if err != nil {
		t.Fatalf("align: %v", err)
	}
	return report
}

func TestCheckTolerancesPass(t *testing.T) {
	tol := Tolerance{StartMs: ms(t, "20ms"), DurationMs: ms(t, "2ms")}
	res, err := CheckTolerances(demoReport(t), tol)
	if err != nil {
		t.Fatal(err)
	}
	if res.Failed() {
		t.Fatalf("expected pass, got violations: %v", res.Violations)
	}
}

func TestCheckTolerancesStartFailure(t *testing.T) {
	tol := Tolerance{StartMs: ms(t, "10ms"), DurationMs: ms(t, "2ms")}
	res, err := CheckTolerances(demoReport(t), tol)
	if err != nil {
		t.Fatal(err)
	}
	if !res.Failed() {
		t.Fatal("12.5ms offset must fail a 10ms start tolerance")
	}
	sawStart := false
	for _, v := range res.Violations {
		if v.Kind != KindStartOffset {
			t.Fatalf("unexpected violation kind %q", v.Kind)
		}
		sawStart = true
		if !strings.Contains(v.Error(), "exceeds tolerance") {
			t.Fatalf("error text unclear: %s", v.Error())
		}
	}
	if !sawStart {
		t.Fatal("no start violation recorded")
	}
}

func TestCheckTolerancesDurationBoundary(t *testing.T) {
	// Max duration delta is exactly 1.000ms: equal to the limit, must pass.
	res, err := CheckTolerances(demoReport(t),
		Tolerance{StartMs: ms(t, "20ms"), DurationMs: ms(t, "1ms")})
	if err != nil {
		t.Fatal(err)
	}
	for _, v := range res.Violations {
		if v.Kind == KindDurationDelta {
			t.Fatalf("1.000ms delta must not exceed a 1.000ms tolerance: %s", v.Error())
		}
	}
	// At 0.5ms it must fail.
	res, err = CheckTolerances(demoReport(t),
		Tolerance{StartMs: ms(t, "20ms"), DurationMs: ms(t, "0.5ms")})
	if err != nil {
		t.Fatal(err)
	}
	sawDur := false
	for _, v := range res.Violations {
		if v.Kind == KindDurationDelta {
			sawDur = true
		}
	}
	if !sawDur {
		t.Fatal("1.000ms delta must fail a 0.5ms duration tolerance")
	}
}

func TestCheckTolerancesErrors(t *testing.T) {
	if _, err := CheckTolerances(nil, Tolerance{}); err == nil {
		t.Fatal("nil report must error")
	}
	bad := Tolerance{StartMs: timebase.Rational{Num: -1, Den: 1}}
	if _, err := CheckTolerances(demoReport(t), bad); err == nil {
		t.Fatal("negative tolerance must error")
	}
}

func TestViolationMessage(t *testing.T) {
	v := Violation{
		Kind: KindStartOffset, RepID: "r1080", RefID: "r360", Number: 1,
		DeltaMs: ms(t, "12.5ms"), LimitMs: ms(t, "10ms"),
	}
	msg := v.Error()
	for _, want := range []string{"start_offset", "r1080", "r360", "12.500", "10.000", "1"} {
		if !strings.Contains(msg, want) {
			t.Errorf("message %q missing %q", msg, want)
		}
	}
}
