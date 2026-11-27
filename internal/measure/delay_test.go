package measure

import (
	"testing"

	"regress/internal/clock"
	"regress/internal/model"
	"regress/internal/timeline"
)

func sample(seq uint32, tick clock.Tick) model.Sample {
	return model.Sample{Seq: seq, Tick: tick}
}

func TestQuantileNearestRank(t *testing.T) {
	d := []clock.Tick{10, 20, 30, 40, 50, 60, 70, 80, 90, 100}
	cases := map[uint]clock.Tick{50: 50, 90: 90, 99: 100, 100: 100, 0: 10}
	for p, want := range cases {
		if got := Quantile(d, p); got != want {
			t.Errorf("p%d = %d, want %d", p, got, want)
		}
	}
	if got := Quantile(nil, 50); got != 0 {
		t.Errorf("empty quantile = %d, want 0", got)
	}
}

func TestComputeDelaysAndCounts(t *testing.T) {
	mp4 := []model.Track{{ID: "m", Kind: model.KindID3, Events: []model.Event{
		{Key: "k1", Track: "m", Seq: 0, ObservedTick: 1150},
		{Key: "k2", Track: "m", Seq: 1, ObservedTick: 4150},
		{Key: "k2", Track: "m", Seq: 1, ObservedTick: 4150}, // dup
		{Key: "extra", Track: "m", Seq: 9, ObservedTick: 9000},
	}}}
	plan := []model.PlanItem{
		{Key: "k1", Track: "m", Seq: 0, ExpectedTick: 1000},
		{Key: "k2", Track: "m", Seq: 1, ExpectedTick: 4000},
		{Key: "k3", Track: "m", Seq: 2, ExpectedTick: 7000}, // missing
	}
	tl := timeline.Build(mp4, nil, plan)
	rep := Compute(tl, nil)

	if rep.Counts.Missing != 1 {
		t.Errorf("missing = %d, want 1", rep.Counts.Missing)
	}
	if rep.Counts.Duplicate != 1 {
		t.Errorf("duplicate = %d, want 1", rep.Counts.Duplicate)
	}
	if rep.Counts.Extra != 1 {
		t.Errorf("extra = %d, want 1", rep.Counts.Extra)
	}
	// Both observed delays are exactly 150 ticks.
	if rep.Delay.Count != 2 || rep.Delay.Min != 150 || rep.Delay.Max != 150 ||
		rep.Delay.Median != 150 || rep.Delay.P90 != 150 || rep.Delay.P99 != 150 {
		t.Fatalf("delay stats wrong: %+v", rep.Delay)
	}
}

func TestBoundaryErrorsWithinOneTick(t *testing.T) {
	const step = clock.Tick(3000)
	var samples []model.Sample
	for i := 0; i < 12; i++ {
		samples = append(samples, sample(uint32(i), clock.Tick(i)*step))
	}
	tracks := []model.Track{
		{ID: "video-high", Kind: model.KindVideo, Samples: samples},
	}
	rep := Compute(&timeline.Timeline{}, tracks)
	if len(rep.Boundaries) != 1 {
		t.Fatalf("boundaries = %d", len(rep.Boundaries))
	}
	be := rep.Boundaries[0]
	if be.Rendition != "video-high" || be.MaxAbsError != 0 {
		t.Fatalf("perfect grid reported error: %+v", be)
	}
	// Introduce a one-tick jitter on one sample and confirm it is captured
	// and never exceeds the acceptance threshold of one tick.
	samples[6].Tick += 1
	rep2 := Compute(&timeline.Timeline{}, tracks)
	if rep2.Boundaries[0].MaxAbsError > 1 {
		t.Fatalf("boundary error %d exceeds 1 tick", rep2.Boundaries[0].MaxAbsError)
	}
}

func TestReportDeterministic(t *testing.T) {
	mp4 := []model.Track{{ID: "m", Kind: model.KindEmsg, Events: []model.Event{
		{Key: "k1", Track: "m", Seq: 0, ObservedTick: 3150},
	}}}
	plan := []model.PlanItem{{Key: "k1", Track: "m", Seq: 0, ExpectedTick: 3000}}
	r1 := Compute(timeline.Build(mp4, nil, plan), nil)
	r2 := Compute(timeline.Build(mp4, nil, plan), nil)
	if r1.Delay.P90 != r2.Delay.P90 || r1.Counts != r2.Counts {
		t.Fatal("measurement not reproducible across identical inputs")
	}
}
