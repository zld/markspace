package index

import (
	"testing"

	"mediad/internal/ebml"
)

func track(types map[uint64]ebml.TrackType) []ebml.Track {
	var out []ebml.Track
	for n, t := range types {
		out = append(out, ebml.Track{Number: n, Type: t})
	}
	return out
}

func buildIndex(t *testing.T) *Index {
	t.Helper()
	ix := New(track(map[uint64]ebml.TrackType{1: ebml.TrackVideo, 2: ebml.TrackAudio}))
	// Video: keyframe every 2s. Audio: every 500ms.
	for i := 0; i < 10; i++ {
		ix.Add(ebml.TrackVideo, ebml.Block{
			Track: 1, Timecode: int64(i * 2000), Keyframe: i%2 == 0,
			Source: ebml.Span{Offset: int64(1000 + i*10), Length: 10},
		}, int64(i)*2_000_000_000)
	}
	for i := 0; i < 40; i++ {
		ix.Add(ebml.TrackAudio, ebml.Block{
			Track: 2, Timecode: int64(i * 500),
			Source: ebml.Span{Offset: int64(4000 + i*5), Length: 5},
		}, int64(i)*500_000_000)
	}
	ix.Finalize()
	return ix
}

func TestOnlyKeyframesIndexed(t *testing.T) {
	ix := buildIndex(t)
	entries := ix.Entries(1)
	if len(entries) != 5 {
		t.Fatalf("video entries = %d, want 5", len(entries))
	}
	for _, e := range entries {
		if e.Kind != KindVideoKeyframe {
			t.Fatalf("non-keyframe indexed: %+v", e)
		}
	}
	// The non-keyframe at 2s must not appear.
	for _, e := range entries {
		if e.TimeNS == 2_000_000_000 {
			t.Fatalf("non-keyframe at 2s was indexed: %+v", e)
		}
	}
}

func TestAtOrBeforeAndNearest(t *testing.T) {
	ix := buildIndex(t)
	e, delta, ok := ix.AtOrBefore(1, 5_000_000_000)
	if !ok || e.TimeNS != 4_000_000_000 || delta != 1_000_000_000 {
		t.Fatalf("AtOrBefore = %+v,%d,%v", e, delta, ok)
	}
	if _, _, ok := ix.AtOrBefore(1, -1); ok {
		t.Fatal("AtOrBefore before first keyframe should miss")
	}
	// Exact hit.
	e, delta, ok = ix.Nearest(1, 4_000_000_000)
	if !ok || e.TimeNS != 4_000_000_000 || delta != 0 {
		t.Fatalf("exact Nearest = %+v,%d", e, delta)
	}
	// Tie prefers the earlier keyframe.
	e, _, _ = ix.Nearest(1, 5_000_000_000)
	if e.TimeNS != 4_000_000_000 {
		t.Fatalf("tie resolved forwards: %+v", e)
	}
}

func TestHitsReport(t *testing.T) {
	ix := buildIndex(t)
	rep := ix.Hits(1, []int64{0, 4e9, 8e9}, 33_333_333)
	if rep.Cuts != 3 || rep.Hits != 3 || rep.HitRate != 1.0 {
		t.Fatalf("hit report = %+v", rep)
	}
	// A cut one frame off still hits within tolerance.
	rep = ix.Hits(1, []int64{0, 4e9 + 33_000_000, 8e9}, 33_333_333)
	if rep.Hits != 3 {
		t.Fatalf("tolerance hit report = %+v", rep)
	}
	// Far off is a miss.
	rep = ix.Hits(1, []int64{0, 4e9 + 400_000_000, 8e9}, 33_333_333)
	if rep.Hits != 2 || rep.HitRate >= 0.99 {
		t.Fatalf("miss report = %+v", rep)
	}
}

func TestSourceSpansRetained(t *testing.T) {
	ix := buildIndex(t)
	entries := ix.Entries(1)
	for i, e := range entries {
		if e.Source.Offset != int64(1000+i*20) {
			t.Fatalf("entry %d source = %+v", i, e.Source)
		}
	}
}

func TestVideoTrackSelection(t *testing.T) {
	ix := New(track(map[uint64]ebml.TrackType{4: ebml.TrackVideo, 2: ebml.TrackVideo, 1: ebml.TrackAudio}))
	got, ok := ix.VideoTrack()
	if !ok || got != 2 {
		t.Fatalf("VideoTrack = %d,%v", got, ok)
	}
}

func TestDeterministicOrdering(t *testing.T) {
	ix := buildIndex(t)
	first := ix.Entries(2)
	for i := 0; i < 5; i++ {
		ix2 := buildIndex(t)
		got := ix2.Entries(2)
		for j := range got {
			if got[j] != first[j] {
				t.Fatalf("entry %d drifted: %+v != %+v", j, got[j], first[j])
			}
		}
	}
}

func TestCutWithin(t *testing.T) {
	ix := buildIndex(t)
	if !ix.CutWithin([]uint64{1}, 4e9, 33_333_333) {
		t.Fatal("exact cut should be within tolerance")
	}
	if ix.CutWithin([]uint64{1}, 4e9+1e9, 33_333_333) {
		t.Fatal("far cut should be outside tolerance")
	}
	if ix.CutWithin([]uint64{99}, 4e9, 33_333_333) {
		t.Fatal("missing track should not be within tolerance")
	}
}
