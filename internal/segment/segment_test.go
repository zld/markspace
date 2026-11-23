package segment

import (
	"errors"
	"testing"

	"mediad/internal/ebml"
)

func buildFixture(t *testing.T, src *testSource) *Manifest {
	t.Helper()
	m, err := Build(BuildInput{
		Representation: "360p",
		VideoTrack:     1,
		AudioTracks:    []uint64{2},
		Index:          fakeIndex(),
		Cuts:           []int64{0, 2e9, 4e9, 6e9},
		Config:         Config{Representations: 1, TargetDuration: 2e9, Tolerance: 33_333_333, TimecodeScale: 1_000_000, FrameNanos: 33_333_333},
		InputDigest:    "abc",
		HashSpan:       src.hashSpan,
	})
	if err != nil {
		t.Fatalf("Build: %v", err)
	}
	return m
}

func TestBuildManifest(t *testing.T) {
	src := newTestSource(4000)
	m := buildFixture(t, src)
	if len(m.Segments) != 3 {
		t.Fatalf("segments = %d, want 3", len(m.Segments))
	}
	for i, s := range m.Segments {
		if s.Index != i {
			t.Fatalf("segment %d index = %d", i, s.Index)
		}
		if s.Hash == "" {
			t.Fatalf("segment %d has no hash", i)
		}
		if s.VideoBlocks != 1 {
			t.Fatalf("segment %d video blocks = %d, want 1", i, s.VideoBlocks)
		}
		if s.AudioBlocks != 4 {
			t.Fatalf("segment %d audio blocks = %d, want 4", i, s.AudioBlocks)
		}
		if s.KeyframeNS != int64(i)*2e9 {
			t.Fatalf("segment %d keyframe = %d", i, s.KeyframeNS)
		}
	}
}

func TestBoundaryBlocksNotDuplicated(t *testing.T) {
	src := newTestSource(4000)
	m := buildFixture(t, src)
	total := 0
	for _, s := range m.Segments {
		total += s.AudioBlocks
	}
	// 6s at 500ms = 12 audio blocks, none double counted.
	if total != 12 {
		t.Fatalf("audio blocks total = %d, want 12", total)
	}
	// The block exactly at 2s must be in segment 1, not 0.
	if m.Segments[1].StartNS != 2e9 {
		t.Fatalf("segment 1 starts at %d", m.Segments[1].StartNS)
	}
	for _, s := range m.Segments {
		if s.AudioBlocks == 0 && s.VideoBlocks == 0 {
			t.Fatalf("empty segment %d", s.Index)
		}
	}
}

func TestDeterministicManifest(t *testing.T) {
	src := newTestSource(4000)
	first := buildFixture(t, src)
	d1, err := ManifestDigest(first)
	if err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 10; i++ {
		again := buildFixture(t, src)
		if err := EqualReports(first, again); err != nil {
			t.Fatalf("run %d: %v", i, err)
		}
		d2, err := ManifestDigest(again)
		if err != nil {
			t.Fatal(err)
		}
		if d1 != d2 {
			t.Fatalf("manifest digest drift: %s != %s", d1, d2)
		}
	}
}

func TestSegmentHashChangesWithBytes(t *testing.T) {
	src := newTestSource(4000)
	m := buildFixture(t, src)
	src2 := newTestSource(4000)
	src2.buf[150] ^= 0xFF // inside segment 0's source range
	m2 := buildFixture(t, src2)
	if m.Segments[0].Hash == m2.Segments[0].Hash {
		t.Fatal("hash did not change with source bytes")
	}
	if m.Segments[2].Hash != m2.Segments[2].Hash {
		t.Fatal("unrelated segment hash changed")
	}
}

func TestConfigDigestDiffers(t *testing.T) {
	a := Config{Representations: 3, TargetDuration: 2e9, Tolerance: 1, TimecodeScale: 1e6, FrameNanos: 1}
	b := a
	b.Representations = 4
	if a.Digest() == b.Digest() {
		t.Fatal("config digest ignores representation count")
	}
	if a.Digest() != a.Digest() {
		t.Fatal("config digest unstable")
	}
}

func TestHashSpansContiguity(t *testing.T) {
	src := newTestSource(100)
	spans := []ebml.Span{{Offset: 10, Length: 10}, {Offset: 20, Length: 10}}
	if _, err := HashSpans(src.hashSpan, spans); err != nil {
		t.Fatalf("contiguous spans rejected: %v", err)
	}
	bad := []ebml.Span{{Offset: 10, Length: 10}, {Offset: 30, Length: 10}}
	if _, err := HashSpans(src.hashSpan, bad); !errors.Is(err, ErrSpanMismatch) {
		t.Fatalf("gap not rejected: %v", err)
	}
}

func TestBuildRejectsEmptyCuts(t *testing.T) {
	src := newTestSource(100)
	_, err := Build(BuildInput{VideoTrack: 1, Index: fakeIndex(), HashSpan: src.hashSpan})
	if !errors.Is(err, ErrNoSegments) {
		t.Fatalf("err = %v", err)
	}
}

func TestBuildSingleCut(t *testing.T) {
	src := newTestSource(4000)
	m, err := Build(BuildInput{
		Representation: "360p", VideoTrack: 1, AudioTracks: []uint64{2},
		Index: fakeIndex(), Cuts: []int64{0}, InputDigest: "x", HashSpan: src.hashSpan,
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(m.Segments) != 1 {
		t.Fatalf("segments = %d, want 1", len(m.Segments))
	}
	if m.Segments[0].DurationNS <= 0 {
		t.Fatalf("open segment has no duration: %+v", m.Segments[0])
	}
}
