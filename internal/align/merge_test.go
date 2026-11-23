package align

import (
	"testing"

	"mediakernel/internal/clock"
	"mediakernel/internal/mediaerr"
)

func frag(source, track, kind string, rank int, idx uint32, startMs, durMs uint64) Fragment {
	start, _ := clock.FromMillis(startMs)
	dur, _ := clock.FromMillis(durMs)
	end, _ := clock.Add(start, dur)
	return Fragment{
		Source: source, TrackID: track, Kind: kind, Rank: rank,
		SegmentIndex: idx, StartTick: start, DurationTick: dur, EndTick: end,
	}
}

func sampleTimeline() []Fragment {
	return []Fragment{
		// intentionally shuffled to prove deterministic ordering
		frag("dash", "a0", "audio", 2, 0, 0, 1000),
		frag("dash", "a0", "audio", 2, 1, 1000, 1000),
		frag("dash", "a0", "audio", 2, 2, 2000, 1000),
		frag("dash", "v0", "video", 0, 0, 0, 1000),
		frag("dash", "v0", "video", 0, 1, 1000, 1000),
		frag("dash", "v0", "video", 0, 2, 2000, 1000),
		frag("dash", "v1", "video", 1, 0, 0, 1000),
		frag("dash", "v1", "video", 1, 1, 1000, 1000),
		frag("dash", "v1", "video", 1, 2, 2000, 1000),
	}
}

func TestMergeDeterministicOrdering(t *testing.T) {
	out, err := Merge(sampleTimeline())
	if err != nil {
		t.Fatal(err)
	}
	// Total order: start tick, then rank, then segment index.
	want := []struct {
		start uint64
		rank  int
		idx   uint32
	}{
		{0, 0, 0}, {0, 1, 0}, {0, 2, 0},
		{90000, 0, 1}, {90000, 1, 1}, {90000, 2, 1},
		{180000, 0, 2}, {180000, 1, 2}, {180000, 2, 2},
	}
	if len(out) != len(want) {
		t.Fatalf("len = %d, want %d", len(out), len(want))
	}
	for i, w := range want {
		if out[i].StartTick != w.start || out[i].Rank != w.rank || out[i].SegmentIndex != w.idx {
			t.Fatalf("position %d = tick %d rank %d idx %d, want %+v",
				i, out[i].StartTick, out[i].Rank, out[i].SegmentIndex, w)
		}
	}
}

func TestMergeRerunStable(t *testing.T) {
	first, err := Merge(sampleTimeline())
	if err != nil {
		t.Fatal(err)
	}
	for run := 0; run < 5; run++ {
		again, err := Merge(sampleTimeline())
		if err != nil {
			t.Fatal(err)
		}
		if len(again) != len(first) {
			t.Fatalf("run %d length changed", run)
		}
		for i := range first {
			if first[i] != again[i] {
				t.Fatalf("run %d fragment %d diverged: %+v vs %+v", run, i, first[i], again[i])
			}
		}
	}
	// Input must not be mutated.
	if sampleTimeline()[0].TrackID != "a0" {
		t.Fatal("Merge modified its input")
	}
}

func TestMergeRejectsConflictingFragment(t *testing.T) {
	in := []Fragment{
		frag("dash", "v0", "video", 0, 0, 0, 1000),
		frag("dash", "v0", "video", 0, 0, 500, 1000), // same key, different start
	}
	_, err := Merge(in)
	me, ok := mediaerr.As(err)
	if !ok || me.Code != "conflicting_fragment" {
		t.Fatalf("got %v, want conflicting_fragment", err)
	}
}

func TestMergeEmpty(t *testing.T) {
	if _, err := Merge(nil); err == nil {
		t.Fatal("empty merge must fail")
	}
}

func TestAlignSyncWithinBudget(t *testing.T) {
	in := []Fragment{
		frag("dash", "v0", "video", 0, 0, 0, 1000),
		frag("dash", "v0", "video", 0, 1, 1000, 1000),
		frag("dash", "a0", "audio", 1, 0, 0, 1000),
		frag("dash", "a0", "audio", 1, 1, 1000, 1000),
	}
	res, err := Align(in, DefaultOptions())
	if err != nil {
		t.Fatalf("aligned grid must pass: %v", err)
	}
	if len(res.Sync) != 2 {
		t.Fatalf("sync pairs = %d, want 2", len(res.Sync))
	}
	if res.MaxSyncTick != 0 || len(res.Gaps) != 0 {
		t.Fatalf("perfect grid: max=%d gaps=%+v", res.MaxSyncTick, res.Gaps)
	}
}

func TestAlignSkewMeasuredAndCapped(t *testing.T) {
	// Audio starts 15 ms after video: within the 20 ms budget but measured.
	in := []Fragment{
		frag("dash", "v0", "video", 0, 0, 0, 1000),
		frag("dash", "a0", "audio", 1, 0, 15, 985),
	}
	res, err := Align(in, DefaultOptions())
	if err != nil {
		t.Fatalf("15ms skew should pass: %v", err)
	}
	if res.MaxSyncTick == 0 || res.Sync[0].ErrorMillis != 15 {
		t.Fatalf("skew not measured: %+v", res.Sync)
	}

	// 25 ms skew exceeds the 20 ms budget and must fail the stage.
	bad := []Fragment{
		frag("dash", "v0", "video", 0, 0, 0, 1000),
		frag("dash", "a0", "audio", 1, 0, 25, 975),
	}
	_, err = Align(bad, DefaultOptions())
	me, ok := mediaerr.As(err)
	if !ok || me.Code != "sync_budget" || me.Category != mediaerr.CatAlignment {
		t.Fatalf("got %v, want sync_budget", err)
	}
}

func TestAlignReportsGaps(t *testing.T) {
	// Video has 3 slots, audio only the middle one; two audio-side gaps.
	in := []Fragment{
		frag("dash", "v0", "video", 0, 0, 0, 1000),
		frag("dash", "v0", "video", 0, 1, 1000, 1000),
		frag("dash", "v0", "video", 0, 2, 2000, 1000),
		frag("dash", "a0", "audio", 1, 1, 1000, 1000),
	}
	res, err := Align(in, DefaultOptions())
	if err != nil {
		t.Fatal(err)
	}
	if len(res.Gaps) != 2 {
		t.Fatalf("gaps = %+v, want 2", res.Gaps)
	}
	for _, g := range res.Gaps {
		if g.Recoverable {
			t.Fatal("alignment gaps default to non-recoverable")
		}
	}
}

func TestAlignRerunIdentical(t *testing.T) {
	in := sampleTimeline()
	r1, err := Align(in, DefaultOptions())
	if err != nil {
		t.Fatal(err)
	}
	r2, err := Align(in, DefaultOptions())
	if err != nil {
		t.Fatal(err)
	}
	if len(r1.Fragments) != len(r2.Fragments) || len(r1.Sync) != len(r2.Sync) ||
		len(r1.Gaps) != len(r2.Gaps) || r1.MaxSyncTick != r2.MaxSyncTick {
		t.Fatal("rerun produced a different result")
	}
}
