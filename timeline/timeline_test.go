package timeline

import (
	"errors"
	"testing"

	"mediapack/model"
)

func TestTimebaseConversion(t *testing.T) {
	tb := Timebase{Numerator: 1, Denominator: 90_000}
	nanos, err := tb.ToNanos(90_000)
	if err != nil {
		t.Fatal(err)
	}
	if nanos != NanosPerSecond {
		t.Fatalf("expected 1s, got %d", nanos)
	}
	ticks, err := tb.FromNanos(NanosPerSecond)
	if err != nil {
		t.Fatal(err)
	}
	if ticks != 90_000 {
		t.Fatalf("expected 90000 ticks, got %d", ticks)
	}
}

func TestUnwrapperWrapReorderDuplicateAndAmbiguity(t *testing.T) {
	u, err := NewUnwrapper("video", RTP90kTimebase(), 16, 8)
	if err != nil {
		t.Fatal(err)
	}
	first, err := u.Observe(65530, "sample=0")
	if err != nil {
		t.Fatal(err)
	}
	if first.Extended != 65530 {
		t.Fatalf("unexpected extension: %d", first.Extended)
	}

	wrapped, err := u.Observe(5, "sample=1")
	if err != nil {
		t.Fatal(err)
	}
	if !wrapped.Wrapped || wrapped.Extended != 65541 {
		t.Fatalf("expected wrap to 65541, got %+v", wrapped)
	}
	if wrapped.Nanos <= first.Nanos {
		t.Fatal("wrapped timestamp must remain monotonic")
	}

	dup, err := u.Observe(5, "sample=1-repeat")
	if err != nil {
		t.Fatal(err)
	}
	if !dup.Duplicate || dup.Extended != wrapped.Extended {
		t.Fatalf("duplicate not recognized: %+v", dup)
	}

	// Move forward so a small backward arrival is inside the reorder window.
	if _, err := u.Observe(20, "sample=2"); err != nil {
		t.Fatal(err)
	}
	ooo, err := u.Observe(18, "sample=2-reordered")
	if err != nil {
		t.Fatal(err)
	}
	if !ooo.OutOfOrder || ooo.Extended >= 65556 {
		t.Fatalf("expected bounded out-of-order extension, got %+v", ooo)
	}

	ambiguous, err := NewUnwrapper("audio", MillisecondTimebase(), 8, 2)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := ambiguous.Observe(20, "offset=10"); err != nil {
		t.Fatal(err)
	}
	_, err = ambiguous.Observe(240, "offset=99")
	var me *model.Error
	if !errors.As(err, &me) || me.Category != model.CategoryAmbiguousTimestamp {
		t.Fatalf("expected ambiguous timestamp model.Error, got %v", err)
	}
	if me.Track != "audio" || me.Source != "offset=99" {
		t.Fatalf("error must retain track/source: %+v", me)
	}
}

func TestGraphRejectsMissingAndCycle(t *testing.T) {
	_, err := NewGraph([]Node{{ID: "sub", DependsOn: []model.TrackID{"video"}}})
	if !model.IsCategory(err, model.CategoryMissingDependency) {
		t.Fatalf("expected missing dependency, got %v", err)
	}

	_, err = NewGraph([]Node{
		{ID: "a", DependsOn: []model.TrackID{"b"}},
		{ID: "b", DependsOn: []model.TrackID{"c"}},
		{ID: "c", DependsOn: []model.TrackID{"a"}},
	})
	if !model.IsCategory(err, model.CategoryDependencyCycle) {
		t.Fatalf("expected cycle, got %v", err)
	}
}

func TestGraphStableTopologicalOrder(t *testing.T) {
	nodes := []Node{
		{ID: "subtitle", DependsOn: []model.TrackID{"video"}},
		{ID: "emsg", DependsOn: []model.TrackID{"audio", "video"}},
		{ID: "audio"},
		{ID: "video"},
	}
	g1, err := NewGraph(nodes)
	if err != nil {
		t.Fatal(err)
	}
	g2, err := NewGraph(nodes)
	if err != nil {
		t.Fatal(err)
	}
	o1, o2 := g1.TopoOrder(), g2.TopoOrder()
	if len(o1) != 4 {
		t.Fatalf("unexpected order: %v", o1)
	}
	for i := range o1 {
		if o1[i] != o2[i] {
			t.Fatalf("topological order not stable: %v vs %v", o1, o2)
		}
	}
	index := map[model.TrackID]int{}
	for i, id := range o1 {
		index[id] = i
	}
	if index["video"] >= index["subtitle"] || index["video"] >= index["emsg"] || index["audio"] >= index["emsg"] {
		t.Fatalf("dependencies not ordered: %v", o1)
	}
}

func TestKeyframeGateBlocksUnreadyBoundary(t *testing.T) {
	gate := NewKeyframeGate()
	err := gate.RequireSeal("subtitle", 4_000, "video-high")
	if !model.IsCategory(err, model.CategoryNotReady) {
		t.Fatalf("expected not-ready, got %v", err)
	}
	gate.MarkReady("video-high", 4_000)
	if err := gate.RequireSeal("subtitle", 4_000, "video-high"); err != nil {
		t.Fatalf("boundary should be ready: %v", err)
	}
}

func TestDedupAndSync(t *testing.T) {
	d := NewDedup()
	e := model.Event{Track: "sub", Kind: model.KindSubtitle, Key: "cue-1", StartNanos: 10}
	if !d.Add(e) || d.Add(e) {
		t.Fatal("event should be accepted exactly once")
	}
	report := MaxAVOffset([]int64{0, 33_333_333}, []int64{2_000_000, 31_333_333})
	if report.Matched != 2 || report.MaxOffsetNanos != 2_000_000 {
		t.Fatalf("unexpected sync report: %+v", report)
	}
}
