package plan

import (
	"errors"
	"testing"
)

func TestEvaluateAcceptsFittingPlan(t *testing.T) {
	// A generous 64 MiB budget must accept a modest segment/worker envelope.
	p := NewPlanner(64 * 1024 * 1024)
	b, err := p.Evaluate(Params{PacketsPerSegment: 8, Workers: 2})
	if err != nil {
		t.Fatalf("fitting plan rejected: %v", err)
	}
	if b.TotalBytes > b.BytesLimit || b.Headroom() < 0 {
		t.Fatalf("accepted plan exceeds budget: total=%d limit=%d", b.TotalBytes, b.BytesLimit)
	}
	if b.PacketsPerSegment != 8 || b.Workers != 2 {
		t.Fatalf("plan parameters lost: %+v", b)
	}
}

func TestEvaluateRejectsWithoutSideEffects(t *testing.T) {
	// A tiny budget cannot hold even one segment; admission must fail and the
	// returned error must be the explicit budget-exceeded sentinel.
	p := NewPlanner(1024)
	_, err := p.Evaluate(Params{PacketsPerSegment: 8, Workers: 2})
	if !errors.Is(err, ErrBudgetExceeded) {
		t.Fatalf("want ErrBudgetExceeded, got %v", err)
	}
}

func TestChooseShrinksSegmentUntilFit(t *testing.T) {
	// Budget sized for a single small segment but not the largest candidate.
	limit := readerBufferBytes + checkpointBytes +
		1*(segmentFootprint(2)+sidecarBufferBytes+hasherStateBytes)
	p := NewPlanner(limit)
	b, err := p.Choose(64, 1)
	if err != nil {
		t.Fatalf("Choose should find a fitting size: %v", err)
	}
	if b.PacketsPerSegment > 2 {
		t.Fatalf("planner did not shrink enough: packets=%d", b.PacketsPerSegment)
	}
}

func TestChooseFailsWhenNothingFits(t *testing.T) {
	p := NewPlanner(8)
	if _, err := p.Choose(16, 4); !errors.Is(err, ErrBudgetExceeded) {
		t.Fatalf("want ErrBudgetExceeded, got %v", err)
	}
}

func TestRejectsInvalidParameters(t *testing.T) {
	p := NewPlanner(1 << 30)
	if _, err := p.Evaluate(Params{PacketsPerSegment: 0, Workers: 2}); err == nil {
		t.Fatal("zero segment size accepted")
	}
	if _, err := p.Evaluate(Params{PacketsPerSegment: 8, Workers: 0}); err == nil {
		t.Fatal("zero workers accepted")
	}
}

func TestFootprintGrowsWithSegmentSize(t *testing.T) {
	if segmentFootprint(8) <= segmentFootprint(4) {
		t.Fatal("larger segment must have larger footprint")
	}
}
