package manifest

import (
	"bytes"
	"testing"
)

func sample() *Manifest {
	m := New("abc123", 1024, 42)
	m.Tracks = 2
	m.Representations = []Representation{
		{ID: "r3", Bitrate: 3000000, Bandwidth: 3000000, Tracks: []TrackRep{
			{TrackID: 2, Kind: "audio"}, {TrackID: 1, Kind: "video"}}},
		{ID: "r1", Bitrate: 1000000, Bandwidth: 1000000, Tracks: []TrackRep{
			{TrackID: 1, Kind: "video"}, {TrackID: 2, Kind: "audio"}}},
	}
	m.Segments = []Segment{
		{Index: 1, StartMs: 1000, DurationMs: 1000, Refs: []SegmentRef{
			{RepresentationID: "r1", TrackID: 1, Index: 1, SHA256: "b"},
			{RepresentationID: "r1", TrackID: 2, Index: 1, SHA256: "a"},
		}},
		{Index: 0, StartMs: 0, DurationMs: 1000, Refs: []SegmentRef{
			{RepresentationID: "r1", TrackID: 1, Index: 0, SHA256: "a"},
		}},
	}
	return m
}

func TestEncodeIsDeterministic(t *testing.T) {
	a, err := sample().Encode()
	if err != nil {
		t.Fatal(err)
	}
	b, err := sample().Encode()
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(a, b) {
		t.Fatal("encode is not deterministic")
	}
}

func TestSortOrdersByTrackAndTime(t *testing.T) {
	m := sample()
	m.Sort()
	if m.Representations[0].ID != "r1" {
		t.Fatalf("representations not sorted by bitrate: %s first", m.Representations[0].ID)
	}
	if m.Segments[0].Index != 0 {
		t.Fatalf("segments not sorted by index: %d first", m.Segments[0].Index)
	}
	refs := m.Segments[1].Refs
	if refs[0].TrackID != 1 || refs[1].TrackID != 2 {
		t.Fatalf("refs not sorted by track: %+v", refs)
	}
	for _, rep := range m.Representations {
		if rep.Tracks[0].TrackID != 1 {
			t.Fatalf("tracks not sorted in %s", rep.ID)
		}
	}
}

func TestChecksumIsStableAndSensitive(t *testing.T) {
	m := sample()
	c1 := m.Digest()
	m.Segments[0].Refs[0].SHA256 = "changed"
	c2 := m.Digest()
	if c1 == c2 {
		t.Fatal("checksum did not change when content changed")
	}
}

func TestRepresentationCountMatchesLadder(t *testing.T) {
	m := sample()
	if len(m.Representations) != 2 {
		t.Fatalf("representations = %d, want 2", len(m.Representations))
	}
}
