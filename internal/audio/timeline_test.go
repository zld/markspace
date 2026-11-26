package audio

import (
	"testing"
)

func TestPlannerPlacesByRealPTSAndDetectsGaps(t *testing.T) {
	p, err := NewPlanner()
	if err != nil {
		t.Fatal(err)
	}
	// segment 0 at t=0 for 0.5 s, then a 0.5 s hole, segment 1 at t=1 s
	if _, err := p.Place(Segment{ID: 0, StartNanos: 0}, 24000); err != nil {
		t.Fatal(err)
	}
	if _, err := p.Place(Segment{ID: 1, StartNanos: 1_000_000_000}, 24000); err != nil {
		t.Fatal(err)
	}
	pl := p.Placements()
	if pl[0].StartSample != 0 || pl[1].StartSample != TargetRate {
		t.Fatalf("placement wrong: %+v", pl)
	}
	spans := p.Spans()
	if len(spans) != 1 {
		t.Fatalf("want 1 gap, got %d", len(spans))
	}
	if spans[0].FromSample != 24000 || spans[0].ToSample != 48000 {
		t.Fatalf("gap bounds wrong: %+v", spans[0])
	}
	if p.TotalSamples() != 72000 {
		t.Fatalf("total samples = %d", p.TotalSamples())
	}
}

func TestPlannerAcceptsOutOfOrderAndSorts(t *testing.T) {
	// Workers may complete in any order; Place must not depend on call order.
	p, _ := NewPlanner()
	if _, err := p.Place(Segment{ID: 1, StartNanos: 1_000_000_000}, 100); err != nil {
		t.Fatal(err)
	}
	if _, err := p.Place(Segment{ID: 0, StartNanos: 0}, 100); err != nil {
		t.Fatalf("out-of-order Place must be allowed: %v", err)
	}
	ordered := p.Placements()
	if ordered[0].SegmentID != 0 || ordered[1].SegmentID != 1 {
		t.Fatalf("sorted order wrong: %+v", ordered)
	}
	if p.TotalSamples() == 0 {
		t.Fatal("total samples must span both segments")
	}
}

func TestPlannerPlacementOrderDeterministic(t *testing.T) {
	p, _ := NewPlanner()
	// monotonic feed; ties on start sample (same PTS, two PIDs) must break
	// deterministically by PID then segment ID
	p.Place(Segment{ID: 5, PID: 0x102, StartNanos: 0}, 10)
	p.Place(Segment{ID: 3, PID: 0x100, StartNanos: 0}, 10)
	p.Place(Segment{ID: 4, PID: 0x101, StartNanos: 0}, 10)
	pl := p.Placements()
	want := []int{3, 4, 5} // sorted by PID 0x100, 0x101, 0x102
	for i, w := range want {
		if pl[i].SegmentID != w {
			t.Fatalf("order[%d] = %d, want %d (full: %+v)", i, pl[i].SegmentID, w, pl)
		}
	}
}

func TestPlannerSampleNanosConsistency(t *testing.T) {
	p, _ := NewPlanner()
	pl, err := p.Place(Segment{ID: 0, StartNanos: 0}, 48)
	if err != nil {
		t.Fatal(err)
	}
	// 48 samples at 48 kHz = 1 ms
	if got := p.TimeBase().SampleToNanos(pl.StartSample + 48); got != 1_000_000 {
		t.Fatalf("sample->nanos = %d", got)
	}
}
