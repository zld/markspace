package timeline

import "testing"

func TestDependencyOrdering(t *testing.T) {
	g := NewGraph()
	// Consumer (video) declared before its dependency (audio).
	mustAdd(t, g, Track{Number: 1, Kind: KindVideo, DependsOn: []uint32{2}})
	mustAdd(t, g, Track{Number: 2, Kind: KindAudio})
	if err := g.Resolve(); err != nil {
		t.Fatalf("resolve: %v", err)
	}
	if len(g.Order) != 2 || g.Order[0] != 2 || g.Order[1] != 1 {
		t.Fatalf("dependency must precede consumer, got %v", g.Order)
	}
}

func TestDependencyCycleReported(t *testing.T) {
	g := NewGraph()
	mustAdd(t, g, Track{Number: 1, DependsOn: []uint32{2}})
	mustAdd(t, g, Track{Number: 2, DependsOn: []uint32{1}})
	if err := g.Resolve(); err == nil || err.Category != CategoryDependency {
		t.Fatalf("want dependency error, got %v", err)
	}
}

func TestUnknownDependencyReported(t *testing.T) {
	g := NewGraph()
	mustAdd(t, g, Track{Number: 1, DependsOn: []uint32{9}})
	if err := g.Resolve(); err == nil || err.Category != CategoryInvalidTrack {
		t.Fatalf("want invalid-track error, got %v", err)
	}
}

func TestTrackLimitEnforced(t *testing.T) {
	g := NewGraph()
	for n := uint32(1); n <= MaxTracks; n++ {
		if err := g.Add(Track{Number: n}); err != nil {
			t.Fatalf("add %d: %v", n, err)
		}
	}
	if err := g.Add(Track{Number: MaxTracks + 1}); err == nil ||
		err.Category != CategoryTrackLimit {
		t.Fatalf("want track-limit error, got %v", err)
	}
}

func TestMergeInterleavedSparseVFR(t *testing.T) {
	g := NewGraph()
	mustAdd(t, g, Track{Number: 1, Kind: KindVideo, DefaultDurationNs: 40_000_000})
	mustAdd(t, g, Track{Number: 2, Kind: KindAudio})
	if err := g.Resolve(); err != nil {
		t.Fatal(err)
	}
	// Non-zero start, variable video pacing, sparse audio, interleaved order.
	in := []Sample{
		{TrackNumber: 2, StartNs: 1_000_000_000, DurationNs: 20_000_000, Offset: 10},
		{TrackNumber: 1, StartNs: 1_000_000_000, DurationNs: 40_000_000, Keyframe: true, Offset: 11},
		{TrackNumber: 1, StartNs: 1_040_000_000, DurationNs: 60_000_000, Offset: 12}, // VFR
		{TrackNumber: 1, StartNs: 1_100_000_000, DurationNs: 40_000_000, Offset: 13},
		{TrackNumber: 2, StartNs: 1_300_000_000, DurationNs: 20_000_000, Offset: 14},
		{TrackNumber: 1, StartNs: 1_020_000_000, DurationNs: 10_000_000, Offset: 15}, // out of order
	}
	m := Merge(g, in, DefaultMergeOptions())
	if len(m.Errors) != 0 {
		t.Fatalf("unexpected merge errors: %v", m.Errors)
	}
	if len(m.Samples) != 6 {
		t.Fatalf("want 6 samples, got %d", len(m.Samples))
	}
	for i := 1; i < len(m.Samples); i++ {
		if m.Samples[i].StartNs < m.Samples[i-1].StartNs {
			t.Fatalf("merged timeline not monotonic at %d", i)
		}
	}
	if m.Clock.MinNs != 1_000_000_000 {
		t.Fatalf("non-zero origin lost: min=%d", m.Clock.MinNs)
	}
	if m.Clock.MaxNs < 1_320_000_000 {
		t.Fatalf("max range wrong: %d", m.Clock.MaxNs)
	}
	if errs := CheckInRange(m); len(errs) != 0 {
		t.Fatalf("range invariant failed: %v", errs)
	}
}

func TestMergeRejectsUnknownTrackAndDuplicate(t *testing.T) {
	g := NewGraph()
	mustAdd(t, g, Track{Number: 1, Kind: KindVideo})
	if err := g.Resolve(); err != nil {
		t.Fatal(err)
	}
	in := []Sample{
		{TrackNumber: 1, StartNs: 0, DurationNs: 10, Offset: 1},
		{TrackNumber: 1, StartNs: 0, DurationNs: 10, Offset: 1}, // exact duplicate
		{TrackNumber: 7, StartNs: 5, DurationNs: 10, Offset: 2},  // unknown track
	}
	m := Merge(g, in, DefaultMergeOptions())
	var sawDup, sawInvalid bool
	for _, e := range m.Errors {
		if e.Category == CategoryDuplicate {
			sawDup = true
		}
		if e.Category == CategoryInvalidTrack {
			sawInvalid = true
		}
	}
	if !sawDup || !sawInvalid {
		t.Fatalf("want duplicate+invalid errors, got %v", m.Errors)
	}
	// Unknown-track sample must not be silently retained.
	for _, s := range m.Samples {
		if s.TrackNumber == 7 {
			t.Fatal("unknown-track sample was retained")
		}
	}
	if len(m.Samples) != 1 {
		t.Fatalf("only the single valid sample should remain, got %d", len(m.Samples))
	}
}

func TestNonMonotonicHardErrorWhenReorderDisabled(t *testing.T) {
	g := NewGraph()
	mustAdd(t, g, Track{Number: 1})
	_ = g.Resolve()
	in := []Sample{
		{TrackNumber: 1, StartNs: 100, DurationNs: 1, Offset: 1},
		{TrackNumber: 1, StartNs: 50, DurationNs: 1, Offset: 2},
	}
	m := Merge(g, in, MergeOptions{AllowReorder: false})
	var found bool
	for _, e := range m.Errors {
		if e.Category == CategoryNonMonotonic {
			found = true
		}
	}
	if !found {
		t.Fatalf("want non-monotonic error, got %v", m.Errors)
	}
}

func mustAdd(t *testing.T, g *Graph, tr Track) {
	t.Helper()
	if err := g.Add(tr); err != nil {
		t.Fatalf("add track %d: %v", tr.Number, err)
	}
}
