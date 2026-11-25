package render

import (
	"testing"

	"archive.example/mediakernel/internal/events"
)

func testEvents() []events.Event {
	return []events.Event{
		{Track: events.TrackSubtitle, Kind: events.KindSubtitle, At: 0, Duration: 500_000_000, Seq: 1, Text: "a"},
		{Track: events.TrackChapter, Kind: events.KindChapter, At: 1_000_000_000, Duration: 1_000_000_000, Seq: 2, Text: "b"},
		{Track: events.TrackID3, Kind: events.KindID3, At: 1_200_000_000, Duration: 100_000_000, Seq: 3, Text: "c"},
		{Track: events.TrackSEI, Kind: events.KindSEI, At: 2_000_000_000, Duration: 400_000_000, Seq: 4},
	}
}

func TestFrameSequenceDeterministic(t *testing.T) {
	cfg := DefaultConfig(0)
	one, err := RenderFrameSequence(cfg, testEvents(), 3_000_000_000)
	if err != nil {
		t.Fatal(err)
	}
	two, err := RenderFrameSequence(cfg, events.SortEvents(testEvents()), 3_000_000_000)
	if err != nil {
		t.Fatal(err)
	}
	if one.SequenceHash() != two.SequenceHash() {
		t.Fatal("frame sequence digest depends on event order")
	}
	for i := range one.Frames {
		if one.Frames[i].SHA256 != two.Frames[i].SHA256 {
			t.Fatalf("frame %d hash differs", i)
		}
	}
}

func TestFrameCountAndTiming(t *testing.T) {
	cfg := DefaultConfig(0)
	seq, err := RenderFrameSequence(cfg, nil, 1_000_000_000)
	if err != nil {
		t.Fatal(err)
	}
	if seq.FrameDurationNS != 40_000_000 {
		t.Fatalf("frame duration %d", seq.FrameDurationNS)
	}
	if len(seq.Frames) != 25 {
		t.Fatalf("frame count %d, want 25 for one second at 25fps", len(seq.Frames))
	}
	for i, f := range seq.Frames {
		if want := int64(i) * 40_000_000; f.AtNS != want {
			t.Fatalf("frame %d at %d want %d", i, f.AtNS, want)
		}
	}
}

func TestFramesDifferAcrossActiveEvents(t *testing.T) {
	cfg := DefaultConfig(0)
	seq, err := RenderFrameSequence(cfg, testEvents(), 3_000_000_000)
	if err != nil {
		t.Fatal(err)
	}
	// A frame inside the subtitle window and one with no active events must
	// not hash the same as a frame with a different track active.
	seen := map[string]int{}
	for _, f := range seq.Frames {
		seen[f.SHA256]++
	}
	if len(seen) < 3 {
		t.Fatalf("expected distinct frame digests, got %d", len(seen))
	}
}

func TestAlignmentWithinOneFrame(t *testing.T) {
	cfg := DefaultConfig(0)
	seq, err := RenderFrameSequence(cfg, testEvents(), 3_000_000_000)
	if err != nil {
		t.Fatal(err)
	}
	if seq.MaxAlignError > seq.FrameDurationNS {
		t.Fatalf("alignment error %d exceeds frame %d", seq.MaxAlignError, seq.FrameDurationNS)
	}
}

func TestAlignmentExactOnEventStart(t *testing.T) {
	// The chapter starts at exactly 1.0s, which is a frame instant (25fps).
	cfg := DefaultConfig(0)
	seq, err := RenderFrameSequence(cfg, testEvents(), 1_500_000_000)
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for _, f := range seq.Frames {
		if f.AtNS == 1_000_000_000 {
			found = true
			if f.AlignError != 0 {
				t.Fatalf("exact-start frame alignment error %d", f.AlignError)
			}
		}
	}
	if !found {
		t.Fatal("frame at 1s missing")
	}
}

func TestPixelStatsBounded(t *testing.T) {
	cfg := DefaultConfig(0)
	seq, err := RenderFrameSequence(cfg, testEvents(), 2_000_000_000)
	if err != nil {
		t.Fatal(err)
	}
	for _, f := range seq.Frames {
		if f.MeanByte < 0 || f.MeanByte > 63 {
			t.Fatalf("mean byte %f out of RGB565 range", f.MeanByte)
		}
		if f.MinByte > f.MaxByte {
			t.Fatalf("min %d > max %d", f.MinByte, f.MaxByte)
		}
		if len(f.Pixels) != cfg.Width*cfg.Height {
			t.Fatalf("pixel count %d", len(f.Pixels))
		}
	}
}

func TestEmptyIntervalProducesNoFrames(t *testing.T) {
	cfg := DefaultConfig(1_000_000_000)
	seq, err := RenderFrameSequence(cfg, nil, 500_000_000)
	if err != nil {
		t.Fatal(err)
	}
	if len(seq.Frames) != 0 {
		t.Fatalf("frames %d", len(seq.Frames))
	}
}

func TestFrameLimitEnforced(t *testing.T) {
	cfg := DefaultConfig(0)
	cfg.MaxFrames = 4
	_, err := RenderFrameSequence(cfg, nil, 10_000_000_000)
	if err == nil || err.Code != events.CodeCapacity {
		t.Fatalf("expected capacity error, got %v", err)
	}
}

func TestInvalidGeometryRejected(t *testing.T) {
	cfg := DefaultConfig(0)
	cfg.Width = 0
	if _, err := RenderFrameSequence(cfg, nil, 1); err == nil {
		t.Fatal("zero width accepted")
	}
	cfg = DefaultConfig(0)
	cfg.FPSNum = 0
	if _, err := RenderFrameSequence(cfg, nil, 1); err == nil {
		t.Fatal("zero fps accepted")
	}
	cfg = DefaultConfig(0)
	cfg.Width, cfg.Height = 1024, 1024
	if _, err := RenderFrameSequence(cfg, nil, 1); err == nil {
		t.Fatal("oversized frame accepted")
	}
}

func TestSegmentHashesDeterministic(t *testing.T) {
	cfg := DefaultConfig(0)
	seq, err := RenderFrameSequence(cfg, testEvents(), 2_000_000_000)
	if err != nil {
		t.Fatal(err)
	}
	a := SegmentHashes(seq, 8)
	b := SegmentHashes(seq, 8)
	if len(a) != len(b) {
		t.Fatal("segment count differs")
	}
	for i := range a {
		if a[i] != b[i] {
			t.Fatalf("segment %d differs", i)
		}
	}
	if len(a) != (len(seq.Frames)+7)/8 {
		t.Fatalf("segment count %d for %d frames", len(a), len(seq.Frames))
	}
}

func TestFrameDurationNS(t *testing.T) {
	if got := FrameDurationNS(25, 1); got != 40_000_000 {
		t.Fatalf("25fps=%d", got)
	}
	if got := FrameDurationNS(30_000, 1_001); got != int64(1_000_000_000)*1001/30000 {
		t.Fatalf("ntsc=%d", got)
	}
	if got := FrameDurationNS(0, 1); got != 0 {
		t.Fatalf("zero=%d", got)
	}
}
