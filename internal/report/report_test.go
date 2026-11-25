package report

import (
	"encoding/json"
	"testing"

	"tracecheck/internal/merge"
	"tracecheck/internal/model"
)

func gapFinding(seq uint32, start, end model.Tick) model.Finding {
	f := model.NewFinding(model.ErrSequenceGap, model.SourceRTP, model.SeverityError,
		model.NewSpan(start, end), "missing sequence %d", seq)
	return f
}

func TestReportBudgetFailure(t *testing.T) {
	b := NewBuilder("damaged", 7, Budget{MaxLossPackets: 2, MaxKeyframeInterval: model.TicksFromMillis(3)})
	b.AddFinding(gapFinding(11, 100, 140))
	b.AddFinding(gapFinding(12, 100, 140))
	b.AddFinding(gapFinding(13, 100, 140)) // 3 losses > budget 2
	b.AddConclusion(Conclusion{
		Item: "packet:11", Sequence: 11, Span: model.NewSpan(100, 140),
		Status: "missing", Codes: []model.ErrorCode{model.ErrSequenceGap},
	})
	b.AddAnomalies([]merge.Anomaly{{
		Span: model.NewSpan(100, 140), LossCount: 3, Reasons: []string{"loss"},
	}})

	r := b.Build(10, 1, 4, 2, false)
	if r.Summary.LossCount != 3 || r.Summary.DuplicateCount != 2 {
		t.Fatalf("summary counts wrong: %+v", r.Summary)
	}
	if r.Summary.LossBudgetPassed || r.Summary.Passed {
		t.Fatal("over-budget report must fail")
	}
	if len(r.Intervals) != 1 || r.Intervals[0].Passed || r.Intervals[0].Deviation != 1 {
		t.Fatalf("interval verdict wrong: %+v", r.Intervals)
	}
	if len(r.Intervals[0].Conclusions) != 1 {
		t.Fatalf("interval must locate its per-item conclusion: %+v", r.Intervals[0].Conclusions)
	}
}

func TestReportKeyframeBudget(t *testing.T) {
	budget := Budget{MaxLossPackets: 10, MaxKeyframeInterval: model.TicksFromMillis(3)}
	b := NewBuilder("damaged", 7, budget)
	b.AddFinding(model.NewFinding(model.ErrKeyframeGap, model.SourceMP4, model.SeverityError,
		model.NewSpan(0, model.TicksFromMillis(10)), "keyframe gap 10ms"))
	r := b.Build(5, 1, 2, 0, false)
	if r.Summary.KeyframeBudgetPassed || r.Summary.Passed {
		t.Fatal("keyframe budget must fail")
	}
	if len(r.Intervals) != 1 || r.Intervals[0].Threshold != "max_keyframe_interval" {
		t.Fatalf("keyframe interval wrong: %+v", r.Intervals)
	}
	if r.Intervals[0].Deviation != int64(model.TicksFromMillis(7)) {
		t.Fatalf("deviation = %d", r.Intervals[0].Deviation)
	}
}

func TestReportCleanPasses(t *testing.T) {
	b := NewBuilder("clean", 7, Budget{MaxLossPackets: 2, MaxKeyframeInterval: model.TicksFromMillis(3)})
	r := b.Build(20, 1, 6, 0, false)
	if !r.Summary.Passed || r.Summary.LossCount != 0 {
		t.Fatalf("clean report must pass: %+v", r.Summary)
	}
}

func TestReportCanceledFails(t *testing.T) {
	b := NewBuilder("cancel", 7, Budget{MaxLossPackets: 5, MaxKeyframeInterval: model.TicksFromMillis(100)})
	r := b.Build(5, 0, 0, 0, true)
	if r.Summary.Passed || !r.Summary.Canceled {
		t.Fatal("canceled report must be marked canceled and not passed")
	}
}

func TestReportJSONDeterministic(t *testing.T) {
	build := func() []byte {
		b := NewBuilder("damaged", 7, Budget{MaxLossPackets: 1, MaxKeyframeInterval: model.TicksFromMillis(3)})
		b.AddFinding(gapFinding(12, 100, 140))
		b.AddFinding(gapFinding(11, 100, 140))
		r := b.Build(8, 1, 3, 1, false)
		out, err := r.JSON()
		if err != nil {
			t.Fatal(err)
		}
		return out
	}
	first, second := build(), build()
	if string(first) != string(second) {
		t.Fatal("report JSON must be identical across runs")
	}
	var decoded map[string]any
	if err := json.Unmarshal(first, &decoded); err != nil {
		t.Fatalf("report must be valid JSON: %v", err)
	}
}
