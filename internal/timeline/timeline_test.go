package timeline

import (
	"testing"

	"mediapack/internal/model"
)

func TestWatermarkAdvancesAndMonotonic(t *testing.T) {
	tl := New(StandardRule(), 4)
	evs := []Event{
		{Seq: 1, Source: "a", TrackID: 1, SampleIndex: 0, OriginalTick: 100, Duration: 10},
		{Seq: 2, Source: "a", TrackID: 1, SampleIndex: 1, OriginalTick: 110, Duration: 10},
		{Seq: 3, Source: "a", TrackID: 1, SampleIndex: 2, OriginalTick: 120, Duration: 10},
	}
	for _, e := range evs {
		if _, err := tl.Ingest(e); err != nil {
			t.Fatal(err)
		}
	}
	if tl.Watermark() != 120 || tl.StableCount() != 3 {
		t.Fatalf("watermark=%d stable=%d", tl.Watermark(), tl.StableCount())
	}
	bs := tl.Boundaries()
	for i := 1; i < len(bs); i++ {
		if bs[i].RepairedTick <= bs[i-1].RepairedTick {
			t.Fatalf("non-monotonic boundary %+v", bs[i])
		}
	}
}

func TestBoundaryKeepsOriginalAndDelta(t *testing.T) {
	tl := New(StandardRule(), 4)
	evs := []Event{
		{Seq: 1, Source: "a", TrackID: 1, SampleIndex: 0, OriginalTick: 100, Duration: 10},
		{Seq: 2, Source: "a", TrackID: 1, SampleIndex: 1, OriginalTick: 50, Duration: 10},
	}
	if _, err := tl.Ingest(evs[0]); err != nil {
		t.Fatal(err)
	}
	b, err := tl.Ingest(evs[1])
	if err != nil {
		t.Fatal(err)
	}
	if b.OriginalTick != 50 {
		t.Fatalf("original overwritten: %+v", b)
	}
	if b.RepairedTick != 101 || b.Delta != 51 || b.Repair != RepairReorder {
		t.Fatalf("repair wrong: %+v", b)
	}
}

func TestDuplicateEventNoDoubleSample(t *testing.T) {
	tl := New(StandardRule(), 4)
	e := Event{Seq: 1, Source: "a", TrackID: 1, SampleIndex: 0, OriginalTick: 100, Duration: 10}
	if _, err := tl.Ingest(e); err != nil {
		t.Fatal(err)
	}
	dup := e
	dup.Seq = 2
	_, err := tl.Ingest(dup)
	me, ok := model.AsMediaError(err)
	if !ok || me.Code != model.CodeOverlap {
		t.Fatalf("want overlap/dup error, got %v", err)
	}
	if tl.StableCount() != 1 {
		t.Fatalf("duplicate created a sample: %d", tl.StableCount())
	}
}

func TestRTPWrapExpandsClockAndKeepsRaw(t *testing.T) {
	tl := New(StandardRule(), 4)
	e1 := Event{Seq: 1, Source: "a", TrackID: 1, SampleIndex: 0,
		OriginalTick: 100, Duration: 10, RTPRaw: 0xfffffff0, HasRTP: true}
	if _, err := tl.Ingest(e1); err != nil {
		t.Fatal(err)
	}
	e2 := Event{Seq: 2, Source: "a", TrackID: 1, SampleIndex: 1,
		OriginalTick: 110, Duration: 10, RTPRaw: 0x00000010, HasRTP: true}
	b, err := tl.Ingest(e2)
	if err != nil {
		t.Fatal(err)
	}
	if tl.WrapCount != 1 || b.Repair != RepairWrap {
		t.Fatalf("wrap not detected: count=%d boundary=%+v", tl.WrapCount, b)
	}
	if b.OriginalTick != 110 {
		t.Fatal("raw timestamp must be preserved on wrap")
	}
}

func TestRTPPacketRoundTrip(t *testing.T) {
	p, err := WrapPacket(0x00000010, 0x100000010)
	if err != nil {
		t.Fatal(err)
	}
	buf, err := p.Marshal()
	if err != nil {
		t.Fatal(err)
	}
	raw, ext, err := ParseWrapPacket(buf)
	if err != nil || raw != 0x10 || ext != 0x100000010 {
		t.Fatalf("round trip raw=%d ext=%d err=%v", raw, ext, err)
	}
}

func TestSourceSwitchGapAndOverlap(t *testing.T) {
	tl := New(StandardRule(), 8)
	events := []Event{
		{Seq: 1, Source: "a", TrackID: 1, SampleIndex: 0, OriginalTick: 0, Duration: 100},
		{Seq: 2, Source: "b", TrackID: 1, SampleIndex: 0, OriginalTick: 5000, Duration: 100},
	}
	for _, e := range events {
		if _, err := tl.Ingest(e); err != nil {
			t.Fatal(err)
		}
	}
	if tl.GapCount != 1 {
		t.Fatalf("gap not counted: %d", tl.GapCount)
	}

	tl2 := New(StandardRule(), 8)
	ov := []Event{
		{Seq: 1, Source: "a", TrackID: 1, SampleIndex: 0, OriginalTick: 0, Duration: 1000},
		{Seq: 2, Source: "b", TrackID: 1, SampleIndex: 0, OriginalTick: 500, Duration: 100},
	}
	for _, e := range ov {
		if _, err := tl2.Ingest(e); err != nil {
			t.Fatal(err)
		}
	}
	if tl2.OverlapCount != 1 || tl2.Boundaries()[1].Repair != RepairOverlap {
		t.Fatalf("overlap handling wrong: %+v", tl2.Boundaries())
	}

	rule := StandardRule()
	rule.RejectOverlap = true
	tl3 := New(rule, 8)
	if _, err := tl3.Ingest(ov[0]); err != nil {
		t.Fatal(err)
	}
	_, err := tl3.Ingest(ov[1])
	me, ok := model.AsMediaError(err)
	if !ok || me.Code != model.CodeOverlap {
		t.Fatalf("want overlap rejection, got %v", err)
	}
}

func TestLateEventTriggersReplayMatchingFullRecompute(t *testing.T) {
	rule := StandardRule()
	base := []Event{
		{Seq: 1, Source: "a", TrackID: 1, SampleIndex: 0, OriginalTick: 100, Duration: 10},
		{Seq: 2, Source: "a", TrackID: 1, SampleIndex: 1, OriginalTick: 110, Duration: 10},
		{Seq: 3, Source: "a", TrackID: 1, SampleIndex: 2, OriginalTick: 120, Duration: 10},
		{Seq: 4, Source: "a", TrackID: 1, SampleIndex: 3, OriginalTick: 130, Duration: 10},
		{Seq: 5, Source: "a", TrackID: 1, SampleIndex: 4, OriginalTick: 140, Duration: 10},
	}
	tl := New(rule, 2)
	for _, e := range base {
		if _, err := tl.Ingest(e); err != nil {
			t.Fatal(err)
		}
	}
	// Late event: unique Seq, explicitly marked late, edge inside stable time.
	late := Event{Seq: 6, Source: "a", TrackID: 1, SampleIndex: 9,
		OriginalTick: 115, Duration: 10, Late: true}
	b, err := tl.Ingest(late)
	if err != nil {
		t.Fatal(err)
	}
	if tl.ReplayCount == 0 {
		t.Fatal("expected at least one replay")
	}
	if b == nil || b.OriginalTick != 115 {
		t.Fatalf("late boundary wrong: %+v", b)
	}

	all := append(append([]Event(nil), base...), late)
	oracle, err := FullRecomputeFromScratch(rule, 2, all)
	if err != nil {
		t.Fatal(err)
	}
	got := tl.Boundaries()
	if len(got) != len(oracle) {
		t.Fatalf("boundary count replay=%d oracle=%d", len(got), len(oracle))
	}
	// The replayed edge must carry a late tag in the stored boundary trail.
	foundLateTag := false
	for i := range oracle {
		if got[i].RepairedTick != oracle[i].RepairedTick ||
			got[i].OriginalTick != oracle[i].OriginalTick ||
			got[i].Delta != oracle[i].Delta {
			t.Fatalf("boundary %d diverges:\n replay=%+v\n oracle=%+v",
				i, got[i], oracle[i])
		}
		if got[i].SampleIndex == late.SampleIndex &&
			containsRepair(got[i].Repair, RepairLate) {
			foundLateTag = true
		}
	}
	if !foundLateTag {
		t.Fatalf("stored late boundary missing %q tag: %+v", RepairLate, got)
	}
}

func containsRepair(repair, want string) bool {
	if repair == want {
		return true
	}
	for i := 0; i+len(want) <= len(repair); i++ {
		if repair[i:i+len(want)] == want {
			return true
		}
	}
	return false
}

func TestDeterminismAcrossRuns(t *testing.T) {
	rule := StandardRule()
	events := []Event{
		{Seq: 1, Source: "a", TrackID: 1, SampleIndex: 0, OriginalTick: 100, Duration: 10},
		{Seq: 3, Source: "a", TrackID: 1, SampleIndex: 2, OriginalTick: 130, Duration: 10},
		{Seq: 2, Source: "a", TrackID: 1, SampleIndex: 1, OriginalTick: 115, Duration: 10},
	}
	run := func() []model.Boundary {
		tl := New(rule, 2)
		for _, e := range events {
			if _, err := tl.Ingest(e); err != nil {
				t.Fatal(err)
			}
		}
		return tl.Boundaries()
	}
	first := run()
	second := run()
	if len(first) != len(second) {
		t.Fatal("non-deterministic boundary count")
	}
	for i := range first {
		if first[i] != second[i] {
			t.Fatalf("run divergence at %d: %+v vs %+v", i, first[i], second[i])
		}
	}
}
