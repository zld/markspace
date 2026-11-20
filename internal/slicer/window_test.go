package slicer

import (
	"context"
	"os"
	"testing"
	"time"

	"hlscheck/internal/fixture"
)

// TestAnalyzeNoKeyframeInWindow forces a stream whose only IDRs are further
// apart than twice the requested target. The analyzer must fail loudly
// instead of emitting a segment starting on a non-keyframe.
func TestAnalyzeNoKeyframeInWindow(t *testing.T) {
	requireFFmpeg(t)
	path := t.TempDir() + "/wide_gop.ts"
	// 6 seconds of video with a 150-frame (6s @25fps) GOP: the only IDR is
	// the very first frame, so a 1s target finds no later cut candidate.
	spec := fixture.Spec{
		Bps: 250000, Duration: 6 * time.Second, FrameRate: 25,
		GOP: 150, OutPath: path,
	}
	if err := fixture.Generate(context.Background(), spec); err != nil {
		t.Fatalf("Generate: %v", err)
	}
	f, err := os.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	if _, err := Analyze(f, 1*time.Second); err == nil {
		t.Fatal("expected ErrNoKeyframeInWindow for wide-GOP stream")
	}
}

// TestAnalyzeMemoryBounded verifies a longer stream yields a plan whose cut
// count tracks segment count (~duration/target) and never frame count.
func TestAnalyzeMemoryBounded(t *testing.T) {
	src := generateStream(t, 400000, 20*time.Second)
	f, err := os.Open(src)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	plan, err := Analyze(f, 2*time.Second)
	if err != nil {
		t.Fatalf("Analyze: %v", err)
	}
	// 20s / 2s target => roughly 10 segments, not 500 frame records.
	if len(plan.Cuts) < 9 || len(plan.Cuts) > 11 {
		t.Fatalf("cut count = %d, want about 10 (segment-scaled, not frame-scaled)",
			len(plan.Cuts))
	}
}
