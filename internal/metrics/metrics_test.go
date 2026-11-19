package metrics

import "testing"

func TestContinuityEmpty(t *testing.T) {
	var m Metrics
	if _, ok := m.Continuity(); ok {
		t.Fatal("empty metrics must report no continuity data")
	}
	if _, ok := m.TargetQuantile(0.5); ok {
		t.Fatal("empty metrics must report no quantile data")
	}
}

func TestContinuityAndRuns(t *testing.T) {
	var m Metrics
	m.RecordPlayed()
	for i := 0; i < 3; i++ {
		m.RecordConceal(i == 2)
	}
	m.RecordPlayed()
	m.RecordConceal(false)
	r, ok := m.Continuity()
	if !ok {
		t.Fatal("expected continuity data")
	}
	// 2 played out of 6 output slots.
	if r < 0.33 || r > 0.34 {
		t.Fatalf("continuity %v", r)
	}
	if m.MaxConcealRun != 3 || m.Unrecoverable != 1 {
		t.Fatalf("run=%d unrec=%d", m.MaxConcealRun, m.Unrecoverable)
	}
}

func TestTargetQuantile(t *testing.T) {
	var m Metrics
	for i := 1; i <= 10; i++ {
		m.Sample(Watermark{TargetDelayMs: float64(i * 10)})
	}
	p50, _ := m.TargetQuantile(0.5)
	p95, _ := m.TargetQuantile(0.95)
	if p50 != 50 && p50 != 60 {
		t.Fatalf("p50 %v", p50)
	}
	if p95 != 100 {
		t.Fatalf("p95 %v", p95)
	}
}
