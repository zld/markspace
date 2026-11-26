package index

import "testing"

func TestDiffReuseAndContainerOnly(t *testing.T) {
	old := Snapshot{SourceSHA: "src1", Tracks: []TrackDigest{{
		TrackID: 1, TrackSHA: "track1", Segments: []SegmentDigest{{Index: 0, SHA: "seg0"}},
	}}}
	if d := Diff(&old, old); !d.ReuseAll || d.Scope != "none" {
		t.Fatalf("same source decision=%+v", d)
	}

	next := old
	next.SourceSHA = "src2"
	d := Diff(&old, next)
	if !d.ContainerOnly || d.RecomputeSamples != 0 || d.Scope != "container" {
		t.Fatalf("container-only decision=%+v", d)
	}
}

func TestDiffOnlyChangedSegments(t *testing.T) {
	old := Snapshot{SourceSHA: "a", Tracks: []TrackDigest{{TrackID: 9, TrackSHA: "t-old", Segments: []SegmentDigest{
		{Index: 0, SHA: "h0"}, {Index: 1, SHA: "h1"}, {Index: 2, SHA: "h2"},
	}}}}
	next := Snapshot{SourceSHA: "b", Tracks: []TrackDigest{{TrackID: 9, TrackSHA: "t-new", Segments: []SegmentDigest{
		{Index: 0, SHA: "h0"}, {Index: 1, SHA: "changed"}, {Index: 2, SHA: "h2"},
	}}}}
	d := Diff(&old, next)
	if d.ContainerOnly {
		t.Fatal("expected content change")
	}
	got := d.ChangedSegments[9]
	if len(got) != 1 || got[0] != 1 || d.RecomputeSamples != 1 || d.Scope != "segment" {
		t.Fatalf("decision=%+v changed=%v", d, got)
	}
}

func TestDiffWithoutHistoryRecomputesAll(t *testing.T) {
	next := Snapshot{Tracks: []TrackDigest{{TrackID: 1, Segments: []SegmentDigest{{Index: 0}, {Index: 1}}}}}
	d := Diff(nil, next)
	if d.Scope != "all" || len(d.ChangedTracks) != 1 || d.RecomputeSamples != 2 {
		t.Fatalf("initial decision=%+v", d)
	}
}
