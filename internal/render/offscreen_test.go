package render

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

func baseInput() Input {
	return Input{
		BlockIndex: 0, StartSample: 0, EndSample: 16,
		PCM: []byte{1, 2, 3, 4}, InputDigest: "abc123",
		PeakDBFS: -18, AlignmentSamples: 1, FramesPerBlock: 4,
		FrameWidth: 8, FrameHeight: 8,
	}
}

func TestRenderDeterministic(t *testing.T) {
	s1, err := Render(baseInput())
	if err != nil {
		t.Fatal(err)
	}
	s2, _ := Render(baseInput())
	if s1.SegmentHash != s2.SegmentHash || s1.PixelMean != s2.PixelMean {
		t.Fatal("rendering not deterministic")
	}
	if len(s1.FrameHashes) != 4 {
		t.Fatalf("frames=%d", len(s1.FrameHashes))
	}
}

func TestRenderChangesWithInputDigest(t *testing.T) {
	a, _ := Render(baseInput())
	in := baseInput()
	in.InputDigest = "deadbeef"
	b, _ := Render(in)
	if a.SegmentHash == b.SegmentHash {
		t.Fatal("segment hash must change when input digest changes")
	}
}

func TestRenderMarksGapAndKeepsSegmentsSeparate(t *testing.T) {
	in := baseInput()
	in.BlockIndex = 2
	in.GapSamples = 4
	in.GapRanges = []GapRange{{StartSample: 4, EndSample: 8}}
	seg, err := Render(in)
	if err != nil {
		t.Fatal(err)
	}
	if !seg.Gap || len(seg.GapRanges) != 1 {
		t.Fatalf("gap not marked: %+v", seg)
	}
	if seg.AlignmentSamples > 1 {
		t.Fatalf("alignment error %d exceeds one sample", seg.AlignmentSamples)
	}
}

func TestNoMergeAcrossGap(t *testing.T) {
	plain := func(i int, start int64) Segment {
		in := baseInput()
		in.BlockIndex = i
		in.StartSample = start
		in.EndSample = start + 16
		s, _ := Render(in)
		return s
	}
	a := plain(0, 0)
	b := plain(1, 16)
	if !CanMerge(a, b) {
		t.Fatal("two clean adjacent segments should be mergeable")
	}
	gapIn := baseInput()
	gapIn.BlockIndex = 1
	gapIn.GapSamples = 4
	gapIn.GapRanges = []GapRange{{StartSample: 16, EndSample: 20}}
	g, _ := Render(gapIn)
	if CanMerge(a, g) {
		t.Fatal("segments must not merge across an input gap")
	}
}

func TestWriteSegmentAtomicAndReusable(t *testing.T) {
	dir := t.TempDir()
	seg, _ := Render(baseInput())
	path, err := WriteSegment(dir, seg)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(path + ".tmp"); !os.IsNotExist(err) {
		t.Fatal("temp file left behind")
	}
	raw, err := os.ReadFile(filepath.Join(dir, "segment-000000.json"))
	if err != nil {
		t.Fatal(err)
	}
	var back Segment
	if err := json.Unmarshal(raw, &back); err != nil {
		t.Fatal(err)
	}
	if back.SegmentHash != seg.SegmentHash {
		t.Fatal("round-trip changed segment hash")
	}
}
