package analyze

import "testing"

func buildVFR(durations []int64, syncAt map[int]bool) []Sample {
	out := make([]Sample, len(durations))
	var t int64
	for i, d := range durations {
		out[i] = Sample{Index: int64(i), TimeStartTicks: t, DurationTicks: d, Sync: syncAt[i]}
		t += d
	}
	return out
}

func TestGOPUsesRealVFRTimings(t *testing.T) {
	durs := []int64{
		40000, 40000, 40000, // GOP 0 -> sync at 3: 120000
		33333, 50000, 41667, // GOP 3 -> sync at 6: 125000
		40000, 40000, 40000,
	}
	sync := map[int]bool{0: true, 3: true, 6: true}
	samples := buildVFR(durs, sync)
	st := GOP(samples)
	if len(st.Intervals) != 2 {
		t.Fatalf("intervals=%v", st.Intervals)
	}
	if st.Intervals[0] != 120000 || st.Intervals[1] != 125000 {
		t.Fatalf("intervals=%v want [120000 125000] (must reflect VFR durations)", st.Intervals)
	}
	if st.MinTicks != 120000 || st.MaxTicks != 125000 {
		t.Fatalf("min/max=%d/%d", st.MinTicks, st.MaxTicks)
	}
}

func TestGOPSingleKeyframeHasNoInterval(t *testing.T) {
	samples := buildVFR([]int64{10, 10, 10}, map[int]bool{0: true})
	st := GOP(samples)
	if len(st.Intervals) != 0 || st.MinTicks != -1 || st.MaxTicks != -1 {
		t.Fatalf("single keyframe stats=%+v", st)
	}
}

func TestDamageRangeBoundedByKeyframes(t *testing.T) {
	durs := []int64{40000, 40000, 40000, 33333, 50000, 41667, 40000, 40000}
	sync := map[int]bool{0: true, 3: true, 6: true}
	samples := buildVFR(durs, sync)

	// Sample 4 lies between keyframes 3 (t=120000) and 6 (t=245000).
	start, end := DamageRange(samples, 4)
	if start != 120000 || end != 245000 {
		t.Fatalf("range=[%d,%d] want [120000,245000]", start, end)
	}

	// Damaged keyframe itself: starts its own GOP, ends at next keyframe.
	start, end = DamageRange(samples, 3)
	if start != 120000 || end != 245000 {
		t.Fatalf("keyframe range=[%d,%d] want [120000,245000]", start, end)
	}
}

func TestDamageRangeTailFallsBackToMediaEnd(t *testing.T) {
	durs := []int64{100, 100, 100}
	sync := map[int]bool{0: true}
	samples := buildVFR(durs, sync)
	start, end := DamageRange(samples, 1)
	if start != 0 {
		t.Fatalf("start=%d want 0", start)
	}
	if end != 300 {
		t.Fatalf("end=%d want last sample end 300", end)
	}
	if s, e := DamageRange(nil, 0); s != -1 || e != -1 {
		t.Fatalf("empty range should be unknown, got %d,%d", s, e)
	}
}
