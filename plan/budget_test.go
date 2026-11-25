package plan

import (
	"testing"

	"mediad/analyze"
	"mediad/media"
)

func iv(index int, start, end int64, pass bool, bytes int64) analyze.Interval {
	return analyze.Interval{
		InputIndex:  index,
		Name:        "seg",
		SampleStart: start,
		SampleEnd:   end,
		QualityPass: pass,
		Bytes:       bytes,
	}
}

func TestDecideAllDelivered(t *testing.T) {
	tb := media.TimeBase{SampleRate: 1000} // 1 sample = 1 ms
	intervals := []analyze.Interval{
		iv(0, 0, 100, true, 50),
		iv(1, 100, 200, true, 50),
	}
	p, err := Decide(intervals, tb, Constraints{BudgetMs: 500, MaxBytes: 4096})
	if err != nil {
		t.Fatal(err)
	}
	for _, it := range p.Items {
		if it.Decision != DecisionDeliver {
			t.Fatalf("item %d = %s, want deliver", it.InputIndex, it.Decision)
		}
	}
	if p.BudgetDeltaMs != 300 || p.BytesDelta != 3996 {
		t.Fatalf("headroom = budget %d bytes %d, want 300/3996", p.BudgetDeltaMs, p.BytesDelta)
	}
}

func TestQualityGateRejectsBeforeBudget(t *testing.T) {
	// The failing segment is also the cheapest; it must still be rejected,
	// and a quality failure must never be "rescued" by spending budget.
	tb := media.TimeBase{SampleRate: 1000}
	intervals := []analyze.Interval{
		iv(0, 0, 100, false, 10),
		iv(1, 100, 200, true, 10),
	}
	p, err := Decide(intervals, tb, Constraints{BudgetMs: 500, MaxBytes: 4096})
	if err != nil {
		t.Fatal(err)
	}
	if p.Items[0].Decision != DecisionRejectQuality {
		t.Fatalf("bad segment = %s, want reject_quality", p.Items[0].Decision)
	}
	if p.Items[1].Decision != DecisionDeliver {
		t.Fatalf("good segment = %s, want deliver", p.Items[1].Decision)
	}
}

func TestBudgetExhaustionPreservesHighQualityPrefix(t *testing.T) {
	// Budget 150ms; first two segments cost 100 each => after delivering the
	// first (100ms), the second does not fit and is skipped, and so is the
	// third even though the third alone would be cheaper.
	tb := media.TimeBase{SampleRate: 1000}
	intervals := []analyze.Interval{
		iv(0, 0, 100, true, 10),
		iv(1, 100, 200, true, 10),
		iv(2, 200, 250, true, 10),
	}
	p, err := Decide(intervals, tb, Constraints{BudgetMs: 150, MaxBytes: 0})
	if err != nil {
		t.Fatal(err)
	}
	want := []string{DecisionDeliver, DecisionSkipBudget, DecisionSkipBudget}
	for i, it := range p.Items {
		if it.Decision != want[i] {
			t.Fatalf("item %d = %s, want %s", i, it.Decision, want[i])
		}
	}
	if p.BudgetDeltaMs != 50 {
		t.Fatalf("remaining budget = %d, want 50", p.BudgetDeltaMs)
	}
}

func TestByteLimitOnlyAppliesAfterBudget(t *testing.T) {
	// Budget ample; byte cap rejects the third segment while the first two
	// (totaling 200 bytes) are retained in input order.
	tb := media.TimeBase{SampleRate: 1000}
	intervals := []analyze.Interval{
		iv(0, 0, 100, true, 100),
		iv(1, 100, 200, true, 100),
		iv(2, 200, 300, true, 100),
	}
	p, err := Decide(intervals, tb, Constraints{BudgetMs: 500, MaxBytes: 250})
	if err != nil {
		t.Fatal(err)
	}
	want := []string{DecisionDeliver, DecisionDeliver, DecisionSkipBytes}
	for i, it := range p.Items {
		if it.Decision != want[i] {
			t.Fatalf("item %d = %s, want %s", i, it.Decision, want[i])
		}
	}
	if p.BytesEstimated != 200 || p.BytesDelta != 50 {
		t.Fatalf("bytes used/delta = %d/%d, want 200/50", p.BytesEstimated, p.BytesDelta)
	}
}

func TestBudgetPriorityBeatsBytes(t *testing.T) {
	// Segment 1 fits both (100ms / 100 bytes). Segment 2 would overflow BOTH
	// constraints at once: budget 150ms and byte cap 150 are both tripped.
	// The recorded reason must be skip_budget because budget outranks bytes.
	tb := media.TimeBase{SampleRate: 1000}
	intervals := []analyze.Interval{
		iv(0, 0, 100, true, 100),
		iv(1, 100, 200, true, 100),
	}
	p, err := Decide(intervals, tb, Constraints{BudgetMs: 150, MaxBytes: 150})
	if err != nil {
		t.Fatal(err)
	}
	if p.Items[0].Decision != DecisionDeliver {
		t.Fatalf("first = %s, want deliver", p.Items[0].Decision)
	}
	if p.Items[1].Decision != DecisionSkipBudget {
		t.Fatalf("second = %s, want skip_budget (budget outranks bytes)", p.Items[1].Decision)
	}
}

func TestDecideDeterministic(t *testing.T) {
	tb := media.TimeBase{SampleRate: 48000}
	intervals := []analyze.Interval{
		iv(0, 0, 4800, true, 100),
		iv(1, 4800, 9600, false, 100),
		iv(2, 9600, 14400, true, 100),
	}
	first, err := Decide(intervals, tb, Constraints{BudgetMs: 500, MaxBytes: 4096})
	if err != nil {
		t.Fatal(err)
	}
	second, err := Decide(intervals, tb, Constraints{BudgetMs: 500, MaxBytes: 4096})
	if err != nil {
		t.Fatal(err)
	}
	for i := range first.Items {
		if first.Items[i].Decision != second.Items[i].Decision ||
			first.Items[i].CostMs != second.Items[i].CostMs {
			t.Fatalf("rerun differs at %d: %+v vs %+v", i, first.Items[i], second.Items[i])
		}
	}
	// 4800 samples at 48kHz = 100ms each; two deliverables => 200ms used.
	if first.BudgetDeltaMs != 300 {
		t.Fatalf("budget delta = %d, want 300", first.BudgetDeltaMs)
	}
}

func TestZeroConstraintsMeanUnbounded(t *testing.T) {
	tb := media.TimeBase{SampleRate: 1000}
	intervals := []analyze.Interval{
		iv(0, 0, 1000, true, 1<<20),
		iv(1, 1000, 2000, true, 1<<20),
	}
	p, err := Decide(intervals, tb, Constraints{})
	if err != nil {
		t.Fatal(err)
	}
	for _, it := range p.Items {
		if it.Decision != DecisionDeliver {
			t.Fatalf("unbounded plan rejected item: %s", it.Decision)
		}
	}
}
