package manifest_test

import (
	"testing"

	"github.com/zld/continuity/internal/clock"
	"github.com/zld/continuity/internal/manifest"
	"github.com/zld/continuity/internal/repair"
)

// repairedTrack builds a 25 Hz track of n samples with exact integer ticks.
func repairedTrack(n int) *repair.RepairedTrack {
	rt := &repair.RepairedTrack{ID: 1, Timescale: 25}
	for i := 0; i < n; i++ {
		rt.Samples = append(rt.Samples, clock.Repaired{
			Value: clock.MustMake(int64(i), 25),
		})
		rt.Locations = append(rt.Locations, repair.Location{Size: 10, Sync: i%5 == 0})
	}
	return rt
}

func TestAlignPerfectManifest(t *testing.T) {
	rt := repairedTrack(20)
	items := manifest.FromTrack(rt, 1)
	res, err := manifest.Align(items, rt)
	if err != nil {
		t.Fatal(err)
	}
	if res.Aligned != len(items) || res.MaxErrorMS != 0 {
		t.Fatalf("perfect alignment: aligned=%d maxErr=%.3f", res.Aligned, res.MaxErrorMS)
	}
	if res.Counts != (manifest.Counts{}) {
		t.Fatalf("unexpected defect counts: %+v", res.Counts)
	}
}

func TestAlignMeasuresSmallErrorExactly(t *testing.T) {
	rt := repairedTrack(20)
	// Each manifest point is shifted by 1/50 s (half a frame).
	items := []manifest.Item{}
	shift := clock.Shift{N: 1, D: 50}
	for i := 0; i < 10; i++ {
		start, _ := shift.Apply(clock.MustMake(int64(i), 25))
		end := start
		items = append(items, manifest.Item{ID: "x", Start: start, End: end})
	}
	res, err := manifest.Align(items, rt)
	if err != nil {
		t.Fatal(err)
	}
	// Exact max error is 1/50 s = 20 ms, never a float-accumulated value.
	if res.MaxError.Cmp(clock.MustMake(1, 50)) != 0 {
		t.Fatalf("max error = %s, want 1/50", res.MaxError)
	}
	if res.MaxErrorMS < 19.99 || res.MaxErrorMS > 20.01 {
		t.Fatalf("ms rendering = %f", res.MaxErrorMS)
	}
}

func TestAlignCountsDefectClasses(t *testing.T) {
	rt := repairedTrack(20) // coverage [0, 19/25]
	items := []manifest.Item{
		{ID: "ok", Start: clock.MustMake(2, 25), End: clock.MustMake(3, 25)},
		{ID: "backward", Start: clock.MustMake(1, 25), End: clock.MustMake(2, 25)},
		{ID: "outside", Start: clock.MustMake(50, 25), End: clock.MustMake(51, 25)},
	}
	res, err := manifest.Align(items, rt)
	if err != nil {
		t.Fatal(err)
	}
	if res.Counts.Backward != 1 {
		t.Fatalf("backward count = %d", res.Counts.Backward)
	}
	if res.Counts.Coverage != 1 {
		t.Fatalf("coverage count = %d", res.Counts.Coverage)
	}
	// Both in-range items (including the backward-ordered one) still align.
	if res.Aligned != 2 {
		t.Fatalf("aligned = %d, want 2", res.Aligned)
	}
}

func TestAlignMissingAndCoverage(t *testing.T) {
	// A track whose first sample is not at t=0 leaves a real leading gap.
	rt := &repair.RepairedTrack{ID: 1, Timescale: 25}
	for i := 1; i <= 10; i++ {
		rt.Samples = append(rt.Samples, clock.Repaired{
			Value: clock.MustMake(int64(i), 25),
		})
		rt.Locations = append(rt.Locations, repair.Location{Size: 10})
	}
	// A target earlier than the first sample has no carrying media sample.
	early := []manifest.Item{
		{ID: "early", Start: clock.MustMake(1, 50), End: clock.MustMake(1, 25)},
	}
	resEarly, err := manifest.Align(early, rt)
	if err != nil {
		t.Fatal(err)
	}
	if resEarly.Counts.Missing != 1 {
		t.Fatalf("early start should be missing: %+v", resEarly.Counts)
	}

	// An item whose end passes the last sample is a coverage inconsistency.
	late := []manifest.Item{
		{ID: "late", Start: clock.MustMake(8, 25), End: clock.MustMake(50, 25)},
	}
	resLate, err := manifest.Align(late, rt)
	if err != nil {
		t.Fatal(err)
	}
	if resLate.Counts.Coverage != 1 {
		t.Fatalf("late end should be coverage: %+v", resLate.Counts)
	}
}

func TestAlignRejectsEmptyTrack(t *testing.T) {
	if _, err := manifest.Align(nil, &repair.RepairedTrack{}); err == nil {
		t.Fatal("empty track must error")
	}
}
