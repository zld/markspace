package pipeline_test

import (
	"context"
	"errors"
	"sync/atomic"
	"testing"
	"time"

	"mediaroute/internal/fixture"
	"mediaroute/internal/model"
	"mediaroute/internal/pipeline"
)

func makeFragments(n int) []model.Fragment {
	out := make([]model.Fragment, n)
	for i := range out {
		out[i] = model.Fragment{Index: i}
	}
	return out
}

func TestConfigNormalizeDefaults(t *testing.T) {
	c, err := (pipeline.Config{}).Normalize()
	if err != nil {
		t.Fatalf("normalize: %v", err)
	}
	if c.QueueCapacity <= 0 || c.MaxBoxBytes <= 0 {
		t.Fatalf("defaults not applied: %+v", c)
	}
}

func TestConfigNormalizeRejectsOutOfRange(t *testing.T) {
	cases := []pipeline.Config{
		{QueueCapacity: 100000},
		{MaxBoxBytes: 1 << 30},
		{MaxBoxBytes: 1},
	}
	for _, cfg := range cases {
		if _, err := cfg.Normalize(); err == nil {
			t.Fatalf("expected rejection for %+v", cfg)
		}
	}
}

func TestMemoryBudgetScalesWithQueue(t *testing.T) {
	a := pipeline.Config{QueueCapacity: 2, MaxBoxBytes: 1 << 20}
	b := pipeline.Config{QueueCapacity: 8, MaxBoxBytes: 1 << 20}
	if b.MemoryBudget() != 4*a.MemoryBudget() {
		t.Fatalf("budget should scale linearly: %d vs %d", a.MemoryBudget(), b.MemoryBudget())
	}
}

func TestRunPassesAllFragmentsInOrder(t *testing.T) {
	frags := makeFragments(50)
	stage := pipeline.Stage{
		Name: "identity",
		Run: func(ctx context.Context, in <-chan model.Fragment, out chan<- model.Fragment) error {
			for f := range in {
				out <- f
			}
			return nil
		},
	}
	got, err := pipeline.Run(context.Background(), pipeline.Config{QueueCapacity: 2}, frags, stage)
	if err != nil {
		t.Fatalf("run: %v", err)
	}
	if len(got) != len(frags) {
		t.Fatalf("got %d fragments, want %d", len(got), len(frags))
	}
	for i := range got {
		if got[i].Index != i {
			t.Fatalf("fragment %d out of order: index %d", i, got[i].Index)
		}
	}
}

func TestRunBackpressureBlocksProducer(t *testing.T) {
	const n = 20
	frags := makeFragments(n)
	var produced int32
	probe := pipeline.Stage{
		Name: "probe",
		Run: func(ctx context.Context, in <-chan model.Fragment, out chan<- model.Fragment) error {
			// Consume only one fragment; the producer must then block on the
			// bounded queue rather than running ahead.
			first, ok := <-in
			if !ok {
				return nil
			}
			atomic.AddInt32(&produced, 1)
			_ = first
			<-ctx.Done()
			return model.NewTaskError(model.CategoryCanceled, "test", "probe released")
		},
	}
	ctx, cancel := context.WithTimeout(context.Background(), 80*time.Millisecond)
	defer cancel()
	start := time.Now()
	_, err := pipeline.Run(ctx, pipeline.Config{QueueCapacity: 1}, frags, probe)
	if err == nil {
		t.Fatal("expected the cancelled probe to surface an error")
	}
	if elapsed := time.Since(start); elapsed > 2*time.Second {
		t.Fatalf("pipeline hung instead of shedding backpressure: %v", elapsed)
	}
	// With capacity 1 and one consumed fragment, the feeder can have queued at
	// most a couple of fragments, never all 20.
	if atomic.LoadInt32(&produced) >= n {
		t.Fatalf("producer was not blocked by backpressure")
	}
}

func TestRunStopsOnDownstreamFailure(t *testing.T) {
	frags := makeFragments(100)
	var consumed int32
	failing := pipeline.Stage{
		Name: "fail",
		Run: func(ctx context.Context, in <-chan model.Fragment, out chan<- model.Fragment) error {
			for range in {
				if atomic.AddInt32(&consumed, 1) == 3 {
					return model.NewTaskError(model.CategoryDownstream, "fail", "stage failed on purpose")
				}
			}
			return nil
		},
	}
	_, err := pipeline.Run(context.Background(), pipeline.Config{QueueCapacity: 2}, frags, failing)
	if err == nil {
		t.Fatal("expected failure to be returned")
	}
	te, ok := model.AsTaskError(err)
	if !ok {
		t.Fatalf("expected TaskError, got %T", err)
	}
	if te.Category != model.CategoryDownstream {
		t.Fatalf("unexpected category %s", te.Category)
	}
	if atomic.LoadInt32(&consumed) > 10 {
		t.Fatalf("pipeline kept reading after failure: %d fragments consumed", consumed)
	}
}

func TestRunPropagatesContextCancellation(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	stage := pipeline.Stage{
		Name: "sleep",
		Run: func(ctx context.Context, in <-chan model.Fragment, out chan<- model.Fragment) error {
			<-ctx.Done()
			return model.NewTaskError(model.CategoryCanceled, "sleep", "cancelled")
		},
	}
	_, err := pipeline.Run(ctx, pipeline.Config{QueueCapacity: 1}, makeFragments(5), stage)
	if err == nil {
		t.Fatal("expected cancellation error")
	}
	if te, ok := model.AsTaskError(err); !ok || te.Category != model.CategoryCanceled {
		t.Fatalf("expected canceled TaskError, got %v", err)
	}
}

func TestRunWrapsNonTaskError(t *testing.T) {
	stage := pipeline.Stage{
		Name: "raw",
		Run: func(ctx context.Context, in <-chan model.Fragment, out chan<- model.Fragment) error {
			for range in {
			}
			return errors.New("raw failure")
		},
	}
	_, err := pipeline.Run(context.Background(), pipeline.Config{QueueCapacity: 1}, makeFragments(1), stage)
	if err == nil {
		t.Fatal("expected error")
	}
	if _, ok := model.AsTaskError(err); !ok {
		t.Fatalf("raw error should be wrapped in a TaskError, got %T", err)
	}
}

// TestProcessorEndToEnd exercises the composed ingest → repair → deliver path
// with real fixtures, including the truncated case's cross-stage recovery.
func TestProcessorEndToEnd(t *testing.T) {
	for _, d := range []fixture.Damage{
		fixture.DamageNone,
		fixture.DamageSequenceGap,
		fixture.DamageTfdtRegression,
		fixture.DamageTruncatedBox,
		fixture.DamageCorruptIndex,
	} {
		t.Run(d.String(), func(t *testing.T) {
			g, err := fixture.Generate(fixture.DefaultParams(17), d)
			if err != nil {
				t.Fatalf("generate: %v", err)
			}
			proc, err := pipeline.NewProcessor(pipeline.Config{QueueCapacity: 2, MaxBoxBytes: 8 << 20})
			if err != nil {
				t.Fatalf("processor: %v", err)
			}
			out, err := proc.Process(context.Background(), g.Bytes, nil)
			if err != nil {
				t.Fatalf("process: %v", err)
			}
			if out.SHA256 == "" {
				t.Fatal("empty digest")
			}
			if len(out.Output) == 0 {
				t.Fatal("no repaired output")
			}
			if d == fixture.DamageTruncatedBox {
				if out.Counts.Truncated != 1 || out.Counts.Recovered < 1 {
					t.Fatalf("want 1 truncated and ≥1 recovered, got %+v", out.Counts)
				}
			}
			if d == fixture.DamageSequenceGap && out.Counts.MFHDGaps != 1 {
				t.Fatalf("want 1 gap, got %d", out.Counts.MFHDGaps)
			}
		})
	}
}

func TestProcessorRejectsMissingInit(t *testing.T) {
	g, err := fixture.Generate(fixture.DefaultParams(17), fixture.DamageMissingInit)
	if err != nil {
		t.Fatalf("generate: %v", err)
	}
	proc, _ := pipeline.NewProcessor(pipeline.DefaultConfig())
	_, err = proc.Process(context.Background(), g.Bytes, nil)
	te, ok := model.AsTaskError(err)
	if !ok || te.Category != model.CategoryMissingInit {
		t.Fatalf("want missing_init, got %v", err)
	}
}
