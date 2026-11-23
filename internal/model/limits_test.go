package model

import "testing"

func TestTrackAndSampleLimits(t *testing.T) {
	// 32-track limit.
	var tracks []*Track
	for i := uint32(1); i <= MaxTracks; i++ {
		tracks = append(tracks, &Track{ID: i, Kind: KindVideo})
	}
	if _, err := NewGraph(tracks); err != nil {
		t.Fatalf("%d tracks should be allowed: %v", MaxTracks, err)
	}
	tracks = append(tracks, &Track{ID: MaxTracks + 1, Kind: KindVideo})
	if _, err := NewGraph(tracks); err == nil {
		t.Fatal("33rd track must be rejected")
	} else if le, ok := err.(*LimitError); !ok || le.Max != MaxTracks {
		t.Fatalf("want LimitError at MaxTracks, got %T %v", err, err)
	}
}

func TestDuplicateTrackIDRejected(t *testing.T) {
	_, err := NewGraph([]*Track{
		{ID: 1, Kind: KindVideo},
		{ID: 1, Kind: KindAudio},
	})
	if err == nil {
		t.Fatal("duplicate track id must be rejected")
	}
	if _, ok := err.(*DuplicateTrackError); !ok {
		t.Fatalf("want DuplicateTrackError, got %T", err)
	}
}

func TestValidateEdgesMissingDependency(t *testing.T) {
	g, err := NewGraph([]*Track{
		{ID: 1, Kind: KindAudio, DependsOn: []uint32{9}},
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := g.ValidateEdges(); err == nil {
		t.Fatal("edge to missing track must be rejected")
	}
}
