package render_test

import (
	"os"
	"path/filepath"
	"testing"

	"archive/internal/fixtures"
	"archive/internal/parse"
	"archive/internal/render"
	"archive/internal/timeline"
)

func buildSegment(t *testing.T, seed int64, trackIdx int) (timeline.Segment, [][]byte) {
	t.Helper()
	dir := t.TempDir()
	paths, _, err := fixtures.Generate(dir, seed)
	if err != nil {
		t.Fatal(err)
	}
	p := paths[trackIdx]
	var tr *parse.Track
	if filepath.Ext(p) == ".mp4" {
		tr, err = parse.ParseMP4File(p)
	} else {
		tr, err = parse.ParseMXFFile(p)
	}
	if err != nil {
		t.Fatal(err)
	}
	aligned, err := timeline.AlignTrack(trackIdx, tr)
	if err != nil {
		t.Fatal(err)
	}
	segs, err := timeline.Compose([]*parse.Track{tr}, [][]timeline.AlignedSample{aligned}, timeline.DefaultPolicy())
	if err != nil {
		t.Fatal(err)
	}
	raw, err := os.ReadFile(p)
	if err != nil {
		t.Fatal(err)
	}
	payloads := make([][]byte, len(tr.Samples))
	for i, s := range tr.Samples {
		payloads[i] = raw[s.Offset : s.Offset+int64(s.Size)]
	}
	return segs[0], payloads
}

func TestRenderDeterministic(t *testing.T) {
	seg, payloads := buildSegment(t, 17, 0)
	dir1, dir2 := t.TempDir(), t.TempDir()
	paths1, err := render.RenderSegment(dir1, seg, payloads)
	if err != nil {
		t.Fatal(err)
	}
	paths2, err := render.RenderSegment(dir2, seg, payloads)
	if err != nil {
		t.Fatal(err)
	}
	if len(paths1) != 3 {
		t.Fatalf("expected 3 frames, got %d", len(paths1))
	}
	d1, err := render.SegmentDigest(0, paths1)
	if err != nil {
		t.Fatal(err)
	}
	d2, err := render.SegmentDigest(0, paths2)
	if err != nil {
		t.Fatal(err)
	}
	if d1 != d2 {
		t.Fatalf("non-deterministic render: %s != %s", d1, d2)
	}
	for i := range paths1 {
		b1, _ := os.ReadFile(paths1[i])
		b2, _ := os.ReadFile(paths2[i])
		if len(b1) == 0 || len(b1) != len(b2) {
			t.Fatalf("frame %d differs", i)
		}
		for j := range b1 {
			if b1[j] != b2[j] {
				t.Fatalf("frame %d differs at byte %d", i, j)
			}
		}
	}
}

func TestRenderDistinctSeedsAndRotations(t *testing.T) {
	segA, payA := buildSegment(t, 17, 0)
	segB, payB := buildSegment(t, 18, 0)
	dA := renderToDigest(t, segA, payA)
	dB := renderToDigest(t, segB, payB)
	if dA == dB {
		t.Fatal("different seeds produced identical segment hash")
	}
	// Rotation 0 (track 0) vs rotation 90 (track 1) must differ.
	seg0, pay0 := buildSegment(t, 17, 0)
	seg1, pay1 := buildSegment(t, 17, 1)
	if renderToDigest(t, seg0, pay0) == renderToDigest(t, seg1, pay1) {
		t.Fatal("different rotations produced identical segment hash")
	}
}

func renderToDigest(t *testing.T, seg timeline.Segment, payloads [][]byte) string {
	t.Helper()
	dir := t.TempDir()
	paths, err := render.RenderSegment(dir, seg, payloads)
	if err != nil {
		t.Fatal(err)
	}
	d, err := render.SegmentDigest(seg.Index, paths)
	if err != nil {
		t.Fatal(err)
	}
	return d
}
