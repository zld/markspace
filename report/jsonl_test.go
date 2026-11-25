package report

import (
	"bytes"
	"encoding/json"
	"strings"
	"testing"

	"mediad/analyze"
	"mediad/media"
	"mediad/merge"
	"mediad/plan"
)

func samplePlan() *plan.Plan {
	return &plan.Plan{
		Items: []plan.Item{
			{InputIndex: 0, Name: "good-a", SampleStart: 0, SampleEnd: 100,
				QualityPass: true, CostMs: 100, BytesEstimated: 50, Decision: plan.DecisionDeliver},
			{InputIndex: 1, Name: "bad-b", SampleStart: 100, SampleEnd: 200,
				QualityPass: false, CostMs: 100, BytesEstimated: 50,
				Decision: plan.DecisionRejectQuality,
				Reasons:  []media.Fault{media.NewFault(media.CatPartialFrame, "tail")}},
			{InputIndex: 2, Name: "big-c", SampleStart: 200, SampleEnd: 300,
				QualityPass: true, CostMs: 100, BytesEstimated: 50, Decision: plan.DecisionSkipBytes},
		},
		BudgetMs:       500,
		BudgetDeltaMs:  300,
		BytesEstimated: 50,
		BytesLimit:     4096,
	}
}

func TestWriteSummaryFields(t *testing.T) {
	var buf bytes.Buffer
	analysis := &analyze.Report{
		SamplesTotal: 300,
		Damage: []analyze.DamageRange{
			{Category: media.CatPartialFrame, SampleStart: 100, SampleEnd: 200},
		},
	}
	if err := Write(&buf, analysis, samplePlan(), nil); err != nil {
		t.Fatal(err)
	}
	lines := strings.Split(strings.TrimRight(buf.String(), "\n"), "\n")
	if len(lines) != 4 { // 1 summary + 3 segments
		t.Fatalf("got %d JSONL lines, want 4", len(lines))
	}

	var summary Summary
	if err := json.Unmarshal([]byte(lines[0]), &summary); err != nil {
		t.Fatal(err)
	}
	if summary.Kind != "summary" || summary.SamplesTotal != 300 {
		t.Fatalf("summary header wrong: %+v", summary)
	}
	if summary.QualityPass {
		t.Fatal("run with a rejected segment must report quality_pass=false")
	}
	if summary.BudgetMs != 500 || summary.BudgetDeltaMs != 300 ||
		summary.BytesEstimated != 50 || summary.BytesLimit != 4096 {
		t.Fatalf("budget/bytes fields wrong: %+v", summary)
	}
	if summary.Decision != "partial_delivery" || summary.Delivered != 1 ||
		summary.Rejected != 1 || summary.Skipped != 1 {
		t.Fatalf("rollup wrong: %+v", summary)
	}
	if len(summary.DamageRanges) != 1 || summary.DamageRanges[0].Category != media.CatPartialFrame {
		t.Fatalf("damage range wrong: %+v", summary.DamageRanges)
	}
}

func TestWriteSegmentLines(t *testing.T) {
	var buf bytes.Buffer
	if err := Write(&buf, &analyze.Report{}, samplePlan(), nil); err != nil {
		t.Fatal(err)
	}
	lines := strings.Split(strings.TrimRight(buf.String(), "\n"), "\n")
	var seg SegmentLine
	if err := json.Unmarshal([]byte(lines[2]), &seg); err != nil { // bad-b
		t.Fatal(err)
	}
	if seg.Decision != plan.DecisionRejectQuality || seg.Reason != string(media.CatPartialFrame) {
		t.Fatalf("bad segment line wrong: %+v", seg)
	}
	var good SegmentLine
	if err := json.Unmarshal([]byte(lines[1]), &good); err != nil {
		t.Fatal(err)
	}
	if good.QualityPass != true || good.Reason != "" {
		t.Fatalf("good segment line wrong: %+v", good)
	}
}

func TestWriteEmptyDamageIsArray(t *testing.T) {
	var buf bytes.Buffer
	if err := Write(&buf, &analyze.Report{}, &plan.Plan{}, nil); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(buf.String(), `"damage_ranges":[]`) {
		t.Fatalf("empty damage must render as [], got: %s", buf.String())
	}
}

func TestWriteDeterministic(t *testing.T) {
	var a, b bytes.Buffer
	an := &analyze.Report{SamplesTotal: 300, Damage: []analyze.DamageRange{}}
	order := &merge.Order{Failures: []merge.Item{{Sequence: 1, Failure: "x"}}}
	if err := Write(&a, an, samplePlan(), order); err != nil {
		t.Fatal(err)
	}
	if err := Write(&b, an, samplePlan(), order); err != nil {
		t.Fatal(err)
	}
	if a.String() != b.String() {
		t.Fatalf("JSONL not deterministic:\n%s\n%s", a.String(), b.String())
	}
	if !strings.Contains(a.String(), `"merge_failures":1`) {
		t.Fatal("merge failure count missing from summary")
	}
}

func TestOverallDecision(t *testing.T) {
	cases := []struct {
		name string
		plan *plan.Plan
		want string
	}{
		{"all", &plan.Plan{Items: []plan.Item{{Decision: plan.DecisionDeliver}}}, "deliver_all"},
		{"partial", &plan.Plan{Items: []plan.Item{
			{Decision: plan.DecisionDeliver}, {Decision: plan.DecisionSkipBudget},
		}}, "partial_delivery"},
		{"none", &plan.Plan{Items: []plan.Item{{Decision: plan.DecisionRejectQuality}}}, "none_deliverable"},
	}
	for _, c := range cases {
		if got := overallDecision(c.plan, 0); got != c.want {
			t.Errorf("%s: got %s want %s", c.name, got, c.want)
		}
	}
}
