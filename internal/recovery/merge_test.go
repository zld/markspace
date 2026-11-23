package recovery

import (
	"strings"
	"testing"

	"archivecheck/internal/index"
	"archivecheck/internal/media"
)

func layerClock(ts uint32) media.Clock { return media.MustClock(ts) }

// threeLayerResults builds aligned layers with a configurable skew on "mid".
func threeLayerResults(skewMs int64) []Result {
	in := linearInput()
	low := Recover(in)
	midIn := linearInput()
	midIn.Layer = "mid"
	midIn.Clock = layerClock(60000)
	midIn.Index = index.Validation{Aligned: []index.Aligned{
		{Entry: index.Entry{PTS: 0, Offset: 0}, Boundary: 0},
		{Entry: index.Entry{PTS: (250 + skewMs) * 60, Offset: 700}, Boundary: 700},
		{Entry: index.Entry{PTS: (500 + skewMs) * 60, Offset: 1500}, Boundary: 1500},
	}}
	mid := Recover(midIn)
	highIn := linearInput()
	highIn.Layer = "high"
	highIn.Clock = layerClock(90000)
	highIn.Index = index.Validation{Aligned: []index.Aligned{
		{Entry: index.Entry{PTS: 0, Offset: 0}, Boundary: 0},
		{Entry: index.Entry{PTS: 250 * 90, Offset: 700}, Boundary: 700},
		{Entry: index.Entry{PTS: 500 * 90, Offset: 1500}, Boundary: 1500},
	}}
	high := Recover(highIn)
	return []Result{low, mid, high}
}

func TestMergeAlignedLayers(t *testing.T) {
	m, err := MergeResult(threeLayerResults(0), MergeOptions{ToleranceMs: 2})
	if err != nil {
		t.Fatal(err)
	}
	if len(m.Conflicts) != 0 || m.DriftMs != 0 {
		t.Fatalf("conflicts=%+v drift=%d", m.Conflicts, m.DriftMs)
	}
	if len(m.Boundaries) != 3 {
		t.Fatalf("boundaries=%+v", m.Boundaries)
	}
	if len(m.Layers) != 3 || m.Layers[0] != "high" || m.Layers[2] != "mid" {
		t.Fatalf("layers=%+v", m.Layers)
	}
	if diff := m.LayerDiffs(); diff["mid"] != 0 {
		t.Fatalf("diffs=%+v", diff)
	}
}

func TestMergeReportsConflictWithoutMajorityOverride(t *testing.T) {
	m, err := MergeResult(threeLayerResults(8), MergeOptions{ToleranceMs: 2})
	if err != nil {
		t.Fatal(err)
	}
	// The first boundary lies at 0 ms for every layer, so only the later two
	// boundaries show the injected skew.
	if len(m.Conflicts) != 2 {
		t.Fatalf("conflicts=%+v", m.Conflicts)
	}
	c := m.Conflicts[0]
	if c.Spread != 8 || c.Tolerance != 2 {
		t.Fatalf("conflict=%+v", c)
	}
	// Every layer's evidence must remain visible in the conflict.
	if c.RefMs != 250 || c.Times["mid"] != 258 || c.Times["low"] != 250 || c.Times["high"] != 250 {
		t.Fatalf("times=%+v", c.Times)
	}
	// The outlier's byte boundary is preserved, not replaced by the majority.
	if _, ok := c.Offsets["mid"]; !ok {
		t.Fatalf("mid evidence dropped: %+v", c.Offsets)
	}
	if m.DriftMs != 8 {
		t.Fatalf("drift=%d", m.DriftMs)
	}
	if diff := m.LayerDiffs(); diff["mid"] != 8 || diff["low"] != 0 {
		t.Fatalf("diffs=%+v", diff)
	}
	found := false
	for _, f := range m.Faults {
		if f.Kind == media.KindBoundaryConflict && strings.Contains(f.Detail, "mid=258ms") {
			found = true
		}
	}
	if !found {
		t.Fatalf("faults=%+v", m.Faults)
	}
}

func TestMergeUnionGaps(t *testing.T) {
	results := threeLayerResults(0)
	results[0].Gaps = append(results[0].Gaps, Gap{StartMs: 500, EndMs: 750, Reason: media.KindGap, Detail: "low hole"})
	results[1].Gaps = append(results[1].Gaps, Gap{StartMs: 500, EndMs: 750, Reason: media.KindGap, Detail: "mid hole"})
	results[2].Gaps = append(results[2].Gaps, Gap{StartMs: 250, EndMs: 500, Reason: media.KindGap, Detail: "high hole"})
	m, err := MergeResult(results, MergeOptions{ToleranceMs: 2})
	if err != nil {
		t.Fatal(err)
	}
	if len(m.UnrecoveredSpans) != 2 {
		t.Fatalf("spans=%+v", m.UnrecoveredSpans)
	}
	if m.UnrecoveredSpans[0].StartMs != 250 || m.UnrecoveredSpans[1].StartMs != 500 {
		t.Fatalf("spans=%+v", m.UnrecoveredSpans)
	}
	if !strings.Contains(m.UnrecoveredSpans[1].Detail, "low") || !strings.Contains(m.UnrecoveredSpans[1].Detail, "mid") {
		t.Fatalf("overlap detail lost: %q", m.UnrecoveredSpans[1].Detail)
	}
}

func TestMergeValidation(t *testing.T) {
	if _, err := MergeResult(nil, MergeOptions{ToleranceMs: 2}); err == nil {
		t.Fatal("empty merge accepted")
	}
	dup := threeLayerResults(0)
	dup[1].Layer = dup[0].Layer
	if _, err := MergeResult(dup, MergeOptions{ToleranceMs: 2}); err == nil {
		t.Fatal("duplicate layer accepted")
	}
	if _, err := MergeResult(threeLayerResults(0), MergeOptions{}); err == nil {
		t.Fatal("zero tolerance accepted")
	}
	if _, err := MergeResult(threeLayerResults(0), MergeOptions{ToleranceMs: 10, AlignWindowMs: 5}); err == nil {
		t.Fatal("align window below tolerance accepted")
	}
	unnamed := threeLayerResults(0)
	unnamed[0].Layer = ""
	if _, err := MergeResult(unnamed, MergeOptions{ToleranceMs: 2}); err == nil {
		t.Fatal("empty layer name accepted")
	}
}

func TestMergeRejectsUnusableOptions(t *testing.T) {
	if _, err := MergeResult(threeLayerResults(0), MergeOptions{ToleranceMs: 2, AlignWindowMs: 1}); err == nil {
		t.Fatal("align window below tolerance accepted")
	}
}

func TestSegmentCount(t *testing.T) {
	if got := SegmentCount(threeLayerResults(0)); got != 9 {
		t.Fatalf("count=%d", got)
	}
}

func TestMergeNonMonotonicAnchorReported(t *testing.T) {
	results := threeLayerResults(0)
	// The low layer reports a regressing anchor. Merge must keep going, drop
	// that anchor from alignment and surface a structured fault.
	results[0].Anchors = []Anchor{{TimeMs: 250, Offset: 700}, {TimeMs: 100, Offset: 300}, {TimeMs: 500, Offset: 1500}}
	m, err := MergeResult(results, MergeOptions{ToleranceMs: 2})
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for _, f := range m.Faults {
		if f.Kind == media.KindNonMonotonic && f.Layer == "low" {
			found = true
			if f.ByteOffset != 300 {
				t.Fatalf("fault=%+v", f)
			}
		}
	}
	if !found {
		t.Fatalf("faults=%+v", m.Faults)
	}
	for _, b := range m.Boundaries {
		if b.Times["low"] == 100 {
			t.Fatalf("regressing anchor was aligned: %+v", b)
		}
	}
}
