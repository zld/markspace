package policy

import (
	"testing"

	"replaycheck/internal/split"
	"replaycheck/internal/timeline"
)

func segs(n int, bytesEach uint64) []split.Segment {
	out := make([]split.Segment, n)
	for i := range out {
		out[i] = split.Segment{
			Index: i,
			Span:  timeline.Span{Start: timeline.Tick(i * 1000), End: timeline.Tick((i + 1) * 1000)},
			Bytes: bytesEach,
		}
		out[i].StartName = "cut"
	}
	return out
}

func TestDecideRespectsByteCeiling(t *testing.T) {
	b := Budget{MaxBytes: 120_000, MaxProcMS: 5_000, MinQuality: 900_000}
	res, conflict, err := Decide(segs(3, 147_456), b, DefaultLadder())
	if err != nil || conflict != nil {
		t.Fatalf("err=%v conflict=%v", err, conflict)
	}
	if res.TotalBytes > b.MaxBytes {
		t.Fatalf("total %d exceeds ceiling %d", res.TotalBytes, b.MaxBytes)
	}
	if res.MinQualityPPM < b.MinQuality {
		t.Fatalf("quality %d below floor", res.MinQualityPPM)
	}
	if res.PolicyDigest == "" {
		t.Fatal("missing policy digest")
	}
}

func TestDecideProcessingBudgetBeforeBytes(t *testing.T) {
	// A very tight processing budget forces downgrades even though the byte
	// ceiling alone would have accepted the top rung.
	b := Budget{MaxBytes: 10_000_000, MaxProcMS: 100, MinQuality: 900_000}
	res, conflict, err := Decide(segs(2, 100_000), b, DefaultLadder())
	if err != nil || conflict != nil {
		t.Fatalf("err=%v conflict=%v", err, conflict)
	}
	if res.TotalProcMS > b.MaxProcMS {
		t.Fatalf("processing %d exceeds cap %d", res.TotalProcMS, b.MaxProcMS)
	}
}

func TestDecideImpossibleReportsFieldAndTradeoff(t *testing.T) {
	b := Budget{MaxBytes: 1, MaxProcMS: 5_000, MinQuality: 900_000}
	res, conflict, err := Decide(segs(3, 147_456), b, DefaultLadder())
	if err != nil {
		t.Fatal(err)
	}
	if res != nil {
		t.Fatal("must not return a result when the envelope is impossible")
	}
	if conflict == nil || conflict.Field != "bytes" {
		t.Fatalf("conflict=%+v", conflict)
	}
	if conflict.Achievable <= 1 || len(conflict.Tradeoffs) != 3 {
		t.Fatalf("conflict must report achievable bytes and tradeoffs: %+v", conflict)
	}
}

func TestDecideQualityFloorUnreachable(t *testing.T) {
	b := Budget{MaxBytes: 1_000_000_000, MaxProcMS: 1_000_000, MinQuality: 995_000}
	_, conflict, err := Decide(segs(2, 1000), b, DefaultLadder())
	if err != nil || conflict == nil || conflict.Field != "quality" {
		t.Fatalf("err=%v conflict=%+v", err, conflict)
	}
}

func TestBudgetValidation(t *testing.T) {
	if err := (Budget{MaxBytes: 0, MaxProcMS: 1, MinQuality: 1}).Validate(); err == nil {
		t.Fatal("expected validation error")
	}
}
