package forward

import (
	"context"
	"testing"
	"time"
)

func TestInjectorDeterministic(t *testing.T) {
	cfg := InjectConfig{Seed: 42, ReorderRate: 0.3, BurstEvery: 5, BurstDelay: 10, DuplicateRate: 0.2}
	plan := func() []Decision {
		g := NewInjector(cfg)
		out := make([]Decision, 30)
		for i := range out {
			out[i] = g.Plan()
		}
		return out
	}
	a, b := plan(), plan()
	for i := range a {
		if a[i] != b[i] {
			t.Fatalf("decision %d diverges for same seed: %+v vs %+v", i, a[i], b[i])
		}
	}
}

func TestInjectorBurstCadence(t *testing.T) {
	g := NewInjector(InjectConfig{Seed: 1, BurstEvery: 4, BurstDelay: 25})
	var bursts int
	for i := 0; i < 12; i++ {
		if d := g.Plan(); d.DelayMS == 25 {
			bursts++
		}
	}
	if bursts != 3 {
		t.Fatalf("burst count = %d, want 3", bursts)
	}
}

func TestInjectorRatesShape(t *testing.T) {
	g := NewInjector(InjectConfig{Seed: 7, ReorderRate: 0.5, DuplicateRate: 0.5})
	var reorder, dup int
	for i := 0; i < 200; i++ {
		d := g.Plan()
		if d.Reorder {
			reorder++
		}
		if d.Duplicate {
			dup++
		}
	}
	// Loose bounds: with seed 7 both should be well away from the edges.
	if reorder < 50 || reorder > 150 || dup < 50 || dup > 150 {
		t.Fatalf("unexpected rates: reorder=%d dup=%d", reorder, dup)
	}
}

func TestDecisionSleepCancellation(t *testing.T) {
	d := Decision{DelayMS: 5000}
	ctx, cancel := context.WithCancel(context.Background())
	go func() { time.Sleep(10 * time.Millisecond); cancel() }()
	start := time.Now()
	if err := d.Sleep(ctx); err == nil {
		t.Fatal("cancelled sleep must return ctx error")
	}
	if time.Since(start) > time.Second {
		t.Fatal("sleep did not return promptly on cancellation")
	}
}
