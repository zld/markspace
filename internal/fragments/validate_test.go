package fragments

import (
	"testing"

	"example.com/assetcheck/internal/media"
	"example.com/assetcheck/internal/timeline"
	"example.com/assetcheck/internal/tracks"
)

func frag(track uint64, order, cluster int, start timeline.Nanosecond, off int64) media.Fragment {
	return media.Fragment{TrackNumber: track, Order: order, Cluster: cluster, Seq: -1,
		Start: start, Offset: off}
}

func asset(frags ...media.Fragment) *media.Asset {
	return &media.Asset{
		Tracks: []tracks.Track{
			{Number: 1, Type: tracks.TypeVideo},
			{Number: 2, Type: tracks.TypeAudio},
		},
		Fragments: frags,
	}
}

func TestAlignedFragmentsPass(t *testing.T) {
	a := asset(frag(1, 0, 0, 0, 100), frag(2, 1, 0, 0, 120))
	rep := Validate(a)
	if !rep.OK() {
		t.Fatalf("problems: %+v", rep.Problems)
	}
	if rep.MaxDeviation != 0 {
		t.Fatalf("deviation = %d", rep.MaxDeviation)
	}
	if rep.ClusterCount != 1 {
		t.Fatalf("clusters = %d", rep.ClusterCount)
	}
}

func TestDeviationBoundaryIsOneMillisecond(t *testing.T) {
	late := frag(2, 1, 0, 1_000_000, 120)
	late.Reference = 0
	a := asset(frag(1, 0, 0, 0, 100), late)
	if rep := Validate(a); !rep.OK() {
		t.Fatalf("exactly 1ms must pass: %+v", rep.Problems)
	}
	a.Fragments[1].Start = 1_000_001
	rep := Validate(a)
	if rep.OK() {
		t.Fatal("1ms + 1ns must fail")
	}
	if rep.Problems[0].Category != timeline.CatFragmentConflict {
		t.Fatalf("category = %s", rep.Problems[0].Category)
	}
	if rep.DeviationAt != 120 {
		t.Fatalf("deviation offset = %d, want the late block at 120", rep.DeviationAt)
	}
}

func TestSequenceGapDuplicateAndOrder(t *testing.T) {
	mk := func(seq int64, off int64, order int) media.Fragment {
		return media.Fragment{TrackNumber: 1, Order: order, Seq: seq, Cluster: 0, Offset: off}
	}
	t.Run("gap", func(t *testing.T) {
		a := asset(mk(0, 100, 0), mk(2, 120, 1))
		a.Tracks = a.Tracks[:1]
		rep := Validate(a)
		if !hasCategory(rep, timeline.CatMissingElement) {
			t.Fatalf("problems: %+v", rep.Problems)
		}
	})
	t.Run("duplicate", func(t *testing.T) {
		a := asset(mk(0, 100, 0), mk(0, 120, 1))
		a.Tracks = a.Tracks[:1]
		if !hasCategory(Validate(a), timeline.CatDuplicate) {
			t.Fatalf("problems: %+v", Validate(a).Problems)
		}
	})
	t.Run("backwards", func(t *testing.T) {
		a := asset(mk(2, 100, 0), mk(1, 120, 1))
		a.Tracks = a.Tracks[:1]
		if !hasCategory(Validate(a), timeline.CatOrder) {
			t.Fatalf("problems: %+v", Validate(a).Problems)
		}
	})
	t.Run("sequence base", func(t *testing.T) {
		a := asset(mk(5, 100, 0))
		a.Tracks = a.Tracks[:1]
		a.SequenceBase = 0
		if !hasCategory(Validate(a), timeline.CatFragmentConflict) {
			t.Fatalf("problems: %+v", Validate(a).Problems)
		}
	})
}

func TestUndeclaredTrackReferenced(t *testing.T) {
	a := &media.Asset{
		Tracks:    []tracks.Track{{Number: 1}},
		Fragments: []media.Fragment{{TrackNumber: 9, Order: 0, Offset: 64}},
	}
	rep := Validate(a)
	if !hasCategory(rep, timeline.CatFragmentConflict) {
		t.Fatalf("problems: %+v", rep.Problems)
	}
	if rep.Problems[0].Tracks[0] != 9 {
		t.Fatalf("tracks = %v", rep.Problems[0].Tracks)
	}
}

func TestBlockPastDuration(t *testing.T) {
	a := asset(frag(1, 0, 0, 5_000_000_000, 10))
	a.Tracks = a.Tracks[:1]
	a.Duration = 1_000_000_000
	rep := Validate(a)
	if !hasCategory(rep, timeline.CatFragmentConflict) {
		t.Fatalf("problems: %+v", rep.Problems)
	}
}

func TestInterleavedOrderPreserved(t *testing.T) {
	// V, A, V, A arrival keeps each track monotonic, so order is valid.
	a := asset(
		frag(1, 0, 0, 0, 100),
		frag(2, 1, 0, 0, 110),
		frag(1, 2, 0, 40_000_000, 120),
		frag(2, 3, 0, 20_000_000, 130),
	)
	if rep := Validate(a); !rep.OK() {
		t.Fatalf("problems: %+v", rep.Problems)
	}
	// A track that jumps back in time is caught even though arrival order is
	// intact.
	a.Fragments[2].Start = -1
	if rep := Validate(a); rep.OK() {
		t.Fatal("backwards in-track time accepted")
	}
}

func TestProblemsSortedByOffset(t *testing.T) {
	a := &media.Asset{
		Tracks: []tracks.Track{{Number: 1}},
		Fragments: []media.Fragment{
			{TrackNumber: 1, Seq: 6, Order: 0, Offset: 900},
			{TrackNumber: 1, Seq: 5, Order: 1, Offset: 100},
		},
		SequenceBase: 0,
	}
	rep := Validate(a)
	if len(rep.Problems) < 2 {
		t.Fatalf("problems: %+v", rep.Problems)
	}
	for i := 1; i < len(rep.Problems); i++ {
		if rep.Problems[i].Offset < rep.Problems[i-1].Offset {
			t.Fatalf("not sorted: %+v", rep.Problems)
		}
	}
}

func hasCategory(rep Report, cat timeline.Category) bool {
	for _, p := range rep.Problems {
		if p.Category == cat {
			return true
		}
	}
	return false
}
