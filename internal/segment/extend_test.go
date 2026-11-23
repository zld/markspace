package segment

import (
	"testing"

	"mediad/internal/ebml"
	"mediad/internal/index"
)

// TestSourceRangeStartingAtZero guards against the "unset" sentinel bug where
// a segment whose first block begins at byte 0 would lose its source range.
func TestSourceRangeStartingAtZero(t *testing.T) {
	ix := index.New([]ebml.Track{{Number: 1, Type: ebml.TrackVideo}})
	ix.Add(ebml.TrackVideo, ebml.Block{
		Track: 1, Keyframe: true,
		Source: ebml.Span{Offset: 0, Length: 20},
	}, 0)
	ix.Add(ebml.TrackVideo, ebml.Block{
		Track: 1, Keyframe: true,
		Source: ebml.Span{Offset: 20, Length: 20},
	}, 1_000_000_000)
	ix.Finalize()
	src := newTestSource(100)
	m, err := Build(BuildInput{
		Representation: "r", VideoTrack: 1, Index: ix,
		Cuts:        []int64{0, 1_000_000_000},
		Config:      Config{Representations: 1, TargetDuration: 1_000_000_000, Tolerance: 33_333_333, FrameNanos: 33_333_333},
		InputDigest: "d", HashSpan: src.hashSpan,
	})
	if err != nil {
		t.Fatal(err)
	}
	if m.Segments[0].SourceStart != 0 || m.Segments[0].SourceEnd != 20 {
		t.Fatalf("segment 0 source range = [%d,%d), want [0,20)", m.Segments[0].SourceStart, m.Segments[0].SourceEnd)
	}
	if m.Segments[0].Hash == "" {
		t.Fatal("segment 0 has no hash")
	}
}
