package timeline

import (
	"reflect"
	"testing"
)

func TestWatermarkBasicAdvanceAndLate(t *testing.T) {
	w := &Watermark{}
	if late := w.Observe(100); late {
		t.Fatal("first observation cannot be late")
	}
	if late := w.Observe(200); late {
		t.Fatal("forward observation must advance")
	}
	if late := w.Observe(150); !late {
		t.Fatal("backward observation must be late")
	}
	if late := w.Observe(200); !late {
		t.Fatal("equal observation must be late")
	}
	lvl, ok := w.Level()
	if !ok || lvl != 200 {
		t.Fatalf("level = %d/%v, want 200", lvl, ok)
	}
	if w.LateCount != 2 || w.AdvanceCount != 2 {
		t.Fatalf("counts wrong: late=%d advance=%d", w.LateCount, w.AdvanceCount)
	}
}

func TestEngineLateDecisionInterval(t *testing.T) {
	e := NewEngine(7)
	times := []uint64{100, 200, 300, 400}
	for _, tm := range times {
		late, _ := e.Push(tm)
		if late {
			t.Fatalf("%d must not be late", tm)
		}
	}
	late, d := e.Push(250)
	if !late || d == nil {
		t.Fatal("250 behind watermark 400 must be late")
	}
	if d.AffectedStart != 300 || d.AffectedEnd != 400 || d.TrackID != 7 {
		t.Fatalf("recompute interval wrong: %+v", d)
	}
	if len(e.LateDecisions()) != 1 {
		t.Fatal("exactly one late decision expected")
	}
	// A late event must not enter the accepted-time history.
	if got := e.AcceptedTimes(); !reflect.DeepEqual(got, times) {
		t.Fatalf("accepted history changed: %v", got)
	}
}

func TestRecomputeDeterministicOrder(t *testing.T) {
	rc := NewRecomputer(3)
	// Arrival order deliberately scrambled relative to DTS.
	rc.Add(&SampleEvent{TrackID: 3, InputOrder: 0, CorrectedDTS: 100})
	rc.Add(&SampleEvent{TrackID: 3, InputOrder: 2, CorrectedDTS: 300})
	rc.Add(&SampleEvent{TrackID: 3, InputOrder: 1, CorrectedDTS: 200})
	rc.Add(&SampleEvent{TrackID: 3, InputOrder: 3, CorrectedDTS: 400})

	d := LateDecision{TrackID: 3, AffectedStart: 200, AffectedEnd: 400}
	evs, rng := rc.Recompute(d)
	if rng.AffectedCount != 3 {
		t.Fatalf("affected = %d, want 3", rng.AffectedCount)
	}
	wantKeys := []int{1, 2, 3}
	if !reflect.DeepEqual(rng.EventKeys, wantKeys) {
		t.Fatalf("keys = %v, want %v", rng.EventKeys, wantKeys)
	}
	var dts []uint64
	for _, ev := range evs {
		dts = append(dts, ev.CorrectedDTS)
	}
	if !reflect.DeepEqual(dts, []uint64{200, 300, 400}) {
		t.Fatalf("recomputed DTS order wrong: %v", dts)
	}
}

func TestRecomputeTieBreaksOnInputOrder(t *testing.T) {
	rc := NewRecomputer(1)
	rc.Add(&SampleEvent{TrackID: 1, InputOrder: 5, CorrectedDTS: 100})
	rc.Add(&SampleEvent{TrackID: 1, InputOrder: 2, CorrectedDTS: 100})
	rc.Add(&SampleEvent{TrackID: 1, InputOrder: 9, CorrectedDTS: 100})
	ordered := rc.OrderedEvents()
	got := []int{ordered[0].InputOrder, ordered[1].InputOrder, ordered[2].InputOrder}
	if !reflect.DeepEqual(got, []int{2, 5, 9}) {
		t.Fatalf("tie must break on InputOrder, got %v", got)
	}
}

func TestRecomputeIdempotentAcrossRuns(t *testing.T) {
	build := func() []int {
		rc := NewRecomputer(4)
		rc.Add(&SampleEvent{TrackID: 4, InputOrder: 0, CorrectedDTS: 100})
		rc.Add(&SampleEvent{TrackID: 4, InputOrder: 1, CorrectedDTS: 300})
		rc.Add(&SampleEvent{TrackID: 4, InputOrder: 2, CorrectedDTS: 200})
		// Re-adding an existing key must be a no-op.
		rc.Add(&SampleEvent{TrackID: 4, InputOrder: 1, CorrectedDTS: 300})
		_, rng := rc.Recompute(LateDecision{TrackID: 4, AffectedStart: 100, AffectedEnd: 300})
		return rng.EventKeys
	}
	first := build()
	second := build()
	if !reflect.DeepEqual(first, second) {
		t.Fatalf("duplicate runs diverged: %v vs %v", first, second)
	}
	if !reflect.DeepEqual(first, []int{0, 2, 1}) {
		t.Fatalf("unexpected deterministic order: %v", first)
	}
}

func TestMediaTimeConversions(t *testing.T) {
	mt := FromMillis(1000, 90000)
	if mt.Value != 90000 || mt.ToMillis() != 1000 {
		t.Fatalf("90kHz conversion wrong: %+v", mt)
	}
	audio := FromMillis(250, 48000)
	if audio.Value != 12000 || audio.ToMillis() != 250 {
		t.Fatalf("48kHz conversion wrong: %+v", audio)
	}
	sum := mt.Add(900)
	if sum.Value != 90900 {
		t.Fatalf("add wrong: %d", sum.Value)
	}
	if _, ok := mt.Sub(1 << 63); ok {
		t.Fatal("underflowing sub must report false")
	}
}

func TestMediaErrorClassification(t *testing.T) {
	err := ErrorAt(ClassBoxTruncated, 4242, "bad box")
	if err.Error() == "" || err.Class != ClassBoxTruncated || err.Offset != 4242 {
		t.Fatalf("error shape wrong: %v", err)
	}
	if AsMediaError(err) != err {
		t.Fatal("AsMediaError must round-trip its own type")
	}
	if AsMediaError(nil) != nil {
		t.Fatal("nil must stay nil")
	}
}
