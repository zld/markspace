package align

import (
	"testing"

	"gamepack/internal/clock"
	perr "gamepack/internal/errors"
	"gamepack/internal/source"
	"gamepack/internal/stream"
)

// rep builds a representation with n units; keyframe ordinals carry the given
// normalized ticks, and every unit gets a normalized sample.
func rep(id string, n int, keyTicks map[int]clock.Tick) *Representation {
	units := make([]stream.Unit, n)
	var bounds []stream.BoundaryEvent
	samples := make([]source.Sample, n)
	for i := 0; i < n; i++ {
		start := int64(i * 10)
		units[i] = stream.Unit{Ordinal: i, Start: start, End: start + 9}
		samples[i] = source.Sample{Ordinal: i, OriginalOffset: start, NormalizedTick: clock.Tick(i * 3000)}
	}
	for ord, tk := range keyTicks {
		units[ord].Key = true
		bounds = append(bounds, stream.BoundaryEvent{
			Ordinal: ord, Offset: units[ord].Start, Length: 9, Key: true,
		})
		samples[ord].NormalizedTick = tk
	}
	return &Representation{
		ID: id, Codec: stream.CodecH264, BitrateKbps: 1000,
		Scan:    &stream.ScanResult{Codec: stream.CodecH264, Units: units, Boundaries: bounds},
		Samples: samples,
	}
}

func TestBuildAlignedLadder(t *testing.T) {
	// Both renditions key at ticks 0 (ord 0) and 6000 (ord 2).
	a := rep("a-1000", 5, map[int]clock.Tick{0: 0, 2: 6000})
	b := rep("b-500", 5, map[int]clock.Tick{0: 0, 2: 6000})
	ladder, err := Build([]*Representation{a, b})
	if err != nil {
		t.Fatalf("build: %v", err)
	}
	if len(ladder.CutPoints) != 2 || len(ladder.Segments) != 2 {
		t.Fatalf("cuts=%d segs=%d, want 2/2", len(ladder.CutPoints), len(ladder.Segments))
	}
	for _, cp := range ladder.CutPoints {
		// The two bitrates must land on the identical normalized tick.
		if got := cp.Normalized; got != 0 && got != 6000 {
			t.Fatalf("unexpected cut tick %d", got)
		}
		if diff := ladder.CutPoints[cp.Index].Normalized; diff != cp.Normalized {
			t.Fatal("cut tick mismatch across representations")
		}
	}
	seg0 := ladder.Segments[0]
	if seg0.Cuts["a-1000"].StartOrdinal != 0 || seg0.Cuts["b-500"].EndOrdinal != 2 {
		t.Fatalf("segment 0 cuts wrong: %+v", seg0.Cuts)
	}
	// Final segment extends to the last unit.
	last := ladder.Segments[1]
	if last.Cuts["a-1000"].EndOrdinal != 5 {
		t.Fatalf("final segment end = %d, want 5", last.Cuts["a-1000"].EndOrdinal)
	}
}

func TestBuildConflictNoCommonCut(t *testing.T) {
	// Reference key tick 6000; second rendition keys at a different tick.
	a := rep("a-1000", 5, map[int]clock.Tick{0: 0, 2: 6000})
	b := rep("b-500", 5, map[int]clock.Tick{0: 0, 3: 9000})
	_, err := Build([]*Representation{a, b})
	pe := perr.AsError(err)
	if pe == nil || pe.Code != perr.CodeBoundaryConflict {
		t.Fatalf("want BOUNDARY_CONFLICT, got %v", err)
	}
}

func TestBuildRejectsUnitSampleMismatch(t *testing.T) {
	a := rep("a-1000", 5, map[int]clock.Tick{0: 0})
	b := rep("b-500", 4, map[int]clock.Tick{0: 0})
	b.Samples = b.Samples[:3] // force sample/unit count divergence
	_, err := Build([]*Representation{a, b})
	if perr.AsError(err) == nil {
		t.Fatal("sample/unit count mismatch must fail")
	}
}

func TestZeroTickDifferenceAtEachCut(t *testing.T) {
	a := rep("a", 6, map[int]clock.Tick{0: 0, 3: 9000})
	b := rep("b", 6, map[int]clock.Tick{0: 0, 3: 9000})
	ladder, err := Build([]*Representation{a, b})
	if err != nil {
		t.Fatal(err)
	}
	for _, cp := range ladder.CutPoints {
		tA := a.Samples[cp.Ordinals["a"]].NormalizedTick
		tB := b.Samples[cp.Ordinals["b"]].NormalizedTick
		if tA.Distance(tB) != 0 {
			t.Fatalf("cut %d tick difference %d, want 0", cp.Index, tA.Distance(tB))
		}
	}
}
