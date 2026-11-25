package chunks_test

import (
	"testing"

	"replaycheck/internal/chunks"
	"replaycheck/internal/clock"
)

func TestValidateContiguousCoveragePasses(t *testing.T) {
	list := chunks.PlanContiguous(0, 0, 100, 4, []int64{10, 20, 30, 40})
	if errs := chunks.Validate(list, 0, 100); len(errs) != 0 {
		t.Fatalf("expected valid chunks, got %+v", errs)
	}
}

func TestValidateOverlapReportsChunkExpectedAndDeviation(t *testing.T) {
	list := []chunks.Chunk{
		{Sequence: 1, StartNS: 0, EndNS: 50, Offset: 100},
		{Sequence: 2, StartNS: 40, EndNS: 100, Offset: 180},
	}
	errs := chunks.Validate(list, 0, 100)
	if len(errs) != 1 {
		t.Fatalf("errors = %+v", errs)
	}
	e := errs[0]
	if e.Code != clock.CodeChunkOverlap || e.ChunkIndex != 2 {
		t.Fatalf("unexpected error %+v", e)
	}
	if e.ExpectedNS != 50 || e.ActualNS != 40 {
		t.Fatalf("boundaries expected=%d actual=%d", e.ExpectedNS, e.ActualNS)
	}
	if d := clock.AbsNS(e.ActualNS - e.ExpectedNS); d != 10 {
		t.Fatalf("deviation = %d, want 10", d)
	}
}

func TestValidateMonotonicAndCoverageFailures(t *testing.T) {
	reversed := []chunks.Chunk{
		{Sequence: 1, StartNS: 0, EndNS: 40},
		{Sequence: 2, StartNS: 30, EndNS: 20},
	}
	errs := chunks.Validate(reversed, 0, 100)
	if len(errs) < 2 {
		t.Fatalf("expected order and overlap/coverage errors, got %+v", errs)
	}

	gap := []chunks.Chunk{
		{Sequence: 1, StartNS: 0, EndNS: 40},
		{Sequence: 2, StartNS: 60, EndNS: 90},
	}
	errs = chunks.Validate(gap, 0, 100)
	kinds := map[clock.Code]bool{}
	for _, e := range errs {
		kinds[e.Code] = true
		if e.ChunkIndex != 2 {
			t.Fatalf("chunk index = %d, want 2", e.ChunkIndex)
		}
	}
	if !kinds[clock.CodeChunkCoverage] {
		t.Fatalf("expected coverage/gap error, got %+v", errs)
	}
}
