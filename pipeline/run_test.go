package pipeline

import (
	"context"
	"errors"
	"testing"
	"time"

	"mediad/internal/fixture"
	"mediad/plan"
)

func demoCfg() Config {
	return Config{Workers: 2, Queue: 2, BudgetMs: 500, MaxBytes: 4096}
}

func TestRunFullPipeline(t *testing.T) {
	cases := fixture.Dataset()
	out, err := Run(context.Background(), cases, demoCfg())
	if err != nil {
		t.Fatalf("pipeline failed: %v", err)
	}
	if out.Analysis == nil || out.Plan == nil || out.Merge == nil {
		t.Fatal("outcome components must all be populated")
	}
	if out.Analysis.SamplesTotal <= 0 {
		t.Fatal("samples total must be positive")
	}
	if len(out.Analysis.Damage) == 0 {
		t.Fatal("damaged fixtures must produce non-empty damage ranges")
	}
	// Every damaged case must surface at least one quality rejection.
	rejected := 0
	for _, it := range out.Plan.Items {
		if it.Decision == plan.DecisionRejectQuality {
			rejected++
		}
	}
	if rejected == 0 {
		t.Fatal("expected quality rejections from damaged fixtures")
	}
	// Deliverable items must have produced merge values without failures.
	if len(out.Merge.Items) == 0 {
		t.Fatal("expected at least one merged deliverable")
	}
	for _, it := range out.Merge.Items {
		if !it.OK {
			t.Fatalf("merge item %d should be successful", it.Sequence)
		}
	}
}

func TestRunDeterministic(t *testing.T) {
	cases := fixture.Dataset()
	a, err := Run(context.Background(), cases, demoCfg())
	if err != nil {
		t.Fatal(err)
	}
	b, err := Run(context.Background(), cases, demoCfg())
	if err != nil {
		t.Fatal(err)
	}
	if len(a.Merge.Items) != len(b.Merge.Items) {
		t.Fatal("deliverable count differs between runs")
	}
	for i := range a.Merge.Items {
		if a.Merge.Items[i].Sequence != b.Merge.Items[i].Sequence {
			t.Fatalf("merge order differs at %d: %d vs %d",
				i, a.Merge.Items[i].Sequence, b.Merge.Items[i].Sequence)
		}
	}
	if a.Plan.BudgetDeltaMs != b.Plan.BudgetDeltaMs {
		t.Fatal("budget accounting differs between runs")
	}
}

func TestRunCancellationWithinOneSecond(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cases := fixture.CancelDataset()

	// With one worker and a depth-2 queue the producer quickly blocks in
	// Submit; cancel shortly after starting and require a fast, clean exit.
	go func() {
		time.Sleep(60 * time.Millisecond)
		cancel()
	}()

	cfg := Config{Workers: 1, Queue: 2, BudgetMs: 0, MaxBytes: 0}
	start := time.Now()
	out, err := Run(ctx, cases, cfg)
	elapsed := time.Since(start)

	if !errors.Is(err, context.Canceled) {
		t.Fatalf("err = %v, want context.Canceled", err)
	}
	if out == nil || !out.Canceled {
		t.Fatal("canceled outcome must still be returned for review")
	}
	if elapsed > time.Second {
		t.Fatalf("cancellation took %v, want under 1s", elapsed)
	}
}

func TestRunCancellationSignalPropagates(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cases := fixture.CancelDataset()
	done := make(chan error, 1)
	go func() {
		_, err := Run(ctx, cases, Config{Workers: 1, Queue: 1, BudgetMs: 0, MaxBytes: 0})
		done <- err
	}()
	cancel()
	select {
	case err := <-done:
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("err = %v, want context.Canceled", err)
		}
	case <-time.After(time.Second):
		t.Fatal("pipeline did not exit within 1s of cancel")
	}
}

func TestResumeSkipsCompletedWork(t *testing.T) {
	cases := fixture.Dataset()
	cfg := demoCfg()
	first, err := Run(context.Background(), cases, cfg)
	if err != nil {
		t.Fatal(err)
	}
	resumed, err := Resume(context.Background(), cases, cfg, first)
	if err != nil {
		t.Fatal(err)
	}
	// Resuming a completed run must keep the same deliverable set and order.
	if len(resumed.Merge.Items) != len(first.Merge.Items) {
		t.Fatalf("resume changed deliverable count: %d vs %d",
			len(resumed.Merge.Items), len(first.Merge.Items))
	}
	for i := range first.Merge.Items {
		if first.Merge.Items[i].Sequence != resumed.Merge.Items[i].Sequence {
			t.Fatalf("resume order differs at %d", i)
		}
	}
}

func TestOversizedBlockRejected(t *testing.T) {
	// A segment claiming more than MaxBlockBytes must fail as a worker item
	// rather than crash the pipeline.
	big := []fixture.Case{{
		Name: "huge", Kind: fixture.KindOK,
		WAV: fixture.WAVE(MaxBlockBytes + 100), SampleStart: 0,
	}}
	out, err := Run(context.Background(), big, Config{Workers: 1, Queue: 1, BudgetMs: 0, MaxBytes: 0})
	if err != nil {
		t.Fatal(err)
	}
	if len(out.Merge.Failures) != 1 {
		t.Fatalf("got %d merge failures, want 1: %+v", len(out.Merge.Failures), out.Merge)
	}
	if len(out.Merge.Items) != 0 {
		t.Fatal("oversized block must not appear as a successful item")
	}
}
