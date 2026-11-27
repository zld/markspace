package index

import (
	"testing"

	"tsrepair/internal/segment"
)

func TestBuilderAdmitsOnlyKeyframeIndexable(t *testing.T) {
	b := NewBuilder(0x100)
	b.AddSample(0, true, true, 0, 100, 0)     // admitted
	b.AddSample(1, false, true, 100, 200, 50) // not keyframe
	b.AddSample(2, true, false, 200, 300, 90) // not indexable
	b.AddSample(3, true, true, 300, 400, 140) // admitted
	got := b.Build()
	if len(got) != 2 || got[0].SampleIndex != 0 || got[1].SampleIndex != 3 {
		t.Fatalf("entries=%+v", got)
	}
}

func TestBuildSortsByStartTime(t *testing.T) {
	b := NewBuilder(0x100)
	b.AddSample(5, true, true, 500, 600, 0)
	b.AddSample(0, true, true, 0, 100, 0)
	b.AddSample(3, true, true, 300, 400, 0)
	got := b.Build()
	for i := 1; i < len(got); i++ {
		if got[i].StartNanos < got[i-1].StartNanos {
			t.Fatal("entries not sorted")
		}
	}
}

func TestLookupHitsByContainment(t *testing.T) {
	idx := NewIndex([]segment.IndexEntry{
		{SampleIndex: 0, StartNanos: 0, EndNanos: 100},
		{SampleIndex: 1, StartNanos: 100, EndNanos: 200},
		{SampleIndex: 2, StartNanos: 200, EndNanos: 300},
	})
	// Boundary semantics: start is inclusive, end is exclusive.
	hitCases := map[int64]int{0: 0, 50: 0, 99: 0, 100: 1, 299: 2}
	for target, wantSample := range hitCases {
		e, ok := idx.Lookup(target)
		if !ok || e.SampleIndex != wantSample {
			t.Errorf("target=%d want sample %d, got %+v ok=%v", target, wantSample, e, ok)
		}
	}
	// Misses: before first, at/after last end.
	for _, target := range []int64{-1, 300, 1000} {
		if _, ok := idx.Lookup(target); ok {
			t.Errorf("target=%d must be a miss", target)
		}
	}
	st := idx.Stats()
	if st.Hits != len(hitCases) || st.Misses != 3 {
		t.Errorf("stats=%+v", st)
	}
	if st.HitRate() != float64(len(hitCases))/float64(len(hitCases)+3) {
		t.Errorf("hit rate wrong: %f", st.HitRate())
	}
}

func TestEmptyIndexAllMiss(t *testing.T) {
	idx := NewIndex(nil)
	if _, ok := idx.Lookup(0); ok {
		t.Fatal("empty index cannot hit")
	}
	if idx.Stats().Misses != 1 {
		t.Fatal("miss not counted")
	}
	if idx.Stats().HitRate() != 0 {
		t.Error("hit rate with misses must be 0")
	}
}

func TestNoQueriesHitRateIsOne(t *testing.T) {
	idx := NewIndex(nil)
	if idx.Stats().HitRate() != 1.0 {
		t.Error("vacuous hit rate should be 1.0")
	}
}
