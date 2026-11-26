package index

import (
	"testing"

	mp4scan "replayctl/internal/mp4"
)

func TestSliceReturnsPreviousRAPAndHalfOpenSamples(t *testing.T) {
	tr := &mp4scan.Track{Kind: "video", Timescale: 90_000}
	for i := 0; i < 10; i++ {
		tr.Samples = append(tr.Samples, mp4scan.Sample{
			Index: uint32(i), DTS: int64(i * 3600), Duration: 3600,
			Sync: i%4 == 0,
		})
	}
	mov := &mp4scan.Movie{Tracks: []*mp4scan.Track{tr}}
	idx, err := Build(mov)
	if err != nil {
		t.Fatal(err)
	}

	// Start at 1.5 frames: previous RAP is frame 0; DTS in [5400,18000) are 2..4.
	rap, samples := idx.Video.Slice(5400, 18000)
	if rap == nil || rap.Index != 0 {
		t.Fatalf("RAP = %#v, want sample 0", rap)
	}
	if len(samples) != 3 || samples[0].Index != 2 || samples[2].Index != 4 {
		t.Fatalf("samples = %#v", samples)
	}

	// End is exclusive: DTS 18000 (frame 5) must not be included.
	if got := idx.Video.SamplesIn(0, 3600); len(got) != 1 || got[0].Index != 0 {
		t.Fatalf("one-frame range = %#v", got)
	}
}

func TestRAPAtStartIsReturned(t *testing.T) {
	tr := &mp4scan.Track{Kind: "video"}
	for i := 0; i < 3; i++ {
		tr.Samples = append(tr.Samples, mp4scan.Sample{Index: uint32(i), DTS: int64(i), Sync: i == 0 || i == 2})
	}
	idx, err := Build(&mp4scan.Movie{Tracks: []*mp4scan.Track{tr}})
	if err != nil {
		t.Fatal(err)
	}
	rap, ok := idx.Video.RandomAccessBefore(2)
	if !ok || rap.Index != 2 {
		t.Fatalf("RAP at exact start = %#v,%v", rap, ok)
	}
}
