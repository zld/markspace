package index

import (
	"bytes"
	"testing"

	"mediakernel/internal/clock"
	"mediakernel/internal/gen"
	"mediakernel/internal/mp4scan"
)

func TestBuildSortsAndMapsTime(t *testing.T) {
	specs := []gen.TrackSpec{
		{ID: 2, Timescale: 2, SampleDelta: 1, SamplesPerChunk: 2, SampleSizes: []uint32{4, 4, 4}},
		{ID: 1, Timescale: 1, SampleDelta: 2, SamplesPerChunk: 2, SampleSizes: []uint32{4, 4}},
	}
	data := gen.BuildMP4(specs)
	c, err := mp4scan.Scan(bytes.NewReader(data))
	if err != nil {
		t.Fatal(err)
	}
	ix, err := Build(c)
	if err != nil {
		t.Fatal(err)
	}
	if ix.Len() != 5 {
		t.Fatalf("entries=%d", ix.Len())
	}
	// Sorted by track first: all track-1 entries precede track-2.
	for i := 0; i+1 < ix.Len(); i++ {
		a, b := ix.Entries[i], ix.Entries[i+1]
		if a.TrackID > b.TrackID {
			t.Fatal("entries not sorted by track")
		}
		if a.TrackID == b.TrackID && clock.Cmp(a.Start, b.Start) > 0 {
			t.Fatal("entries not sorted by time within track")
		}
	}
	// Track 2 at timescale 2: sample 1 starts at 1/2 s.
	e := ix.Track(2)[1]
	if e.Start.Num != 1 || e.Start.Den != 2 {
		t.Fatalf("start=%d/%d", e.Start.Num, e.Start.Den)
	}
	if e.End.Num != 1 || e.End.Den != 1 {
		t.Fatalf("end=%d/%d want 1/1", e.End.Num, e.End.Den)
	}
	if got := ix.Tracks(); len(got) != 2 || got[0] != 1 || got[1] != 2 {
		t.Fatalf("tracks=%v", got)
	}
	if ix.Track(99) != nil {
		t.Fatal("unknown track must be nil")
	}
}
