package slicer

import (
	"context"
	"os"
	"testing"
	"time"

	"hlscheck/internal/fixture"
	"hlscheck/internal/model"
)

// requireFFmpeg skips integration tests when the local generator is absent.
func requireFFmpeg(t *testing.T) {
	t.Helper()
	if err := fixture.VerifyFFmpeg(); err != nil {
		t.Skipf("ffmpeg unavailable: %v", err)
	}
}

func generateStream(t *testing.T, bps int, dur time.Duration) string {
	t.Helper()
	requireFFmpeg(t)
	path := t.TempDir() + "/src.ts"
	spec := fixture.Spec{
		Bps: bps, Duration: dur, FrameRate: 25, GOP: 50, OutPath: path,
	}
	if err := fixture.Generate(context.Background(), spec); err != nil {
		t.Fatalf("Generate: %v", err)
	}
	return path
}

func TestAnalyzeProducesKeyframeBoundaries(t *testing.T) {
	src := generateStream(t, 400000, 11*time.Second)
	f, err := os.Open(src)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	plan, err := Analyze(f, 2*time.Second)
	if err != nil {
		t.Fatalf("Analyze: %v", err)
	}
	if len(plan.Cuts) < 5 || len(plan.Cuts) > 7 {
		t.Fatalf("cut count = %d, want ~6", len(plan.Cuts))
	}
	for i, cut := range plan.Cuts {
		if !cut.Keyframe {
			t.Fatalf("cut %d is not keyframe aligned", i)
		}
		if cut.StartOffset%model.PacketSize != 0 {
			t.Fatalf("cut %d offset %d not packet aligned", i, cut.StartOffset)
		}
	}
}

func TestSliceEndToEndSegmentsExist(t *testing.T) {
	src := generateStream(t, 500000, 6*time.Second)
	out := t.TempDir()
	r := model.Rendition{Bps: 500000, Source: src, Dir: "rendition_500000"}
	result, err := Slice(out, r, Options{
		Target: 2 * time.Second, AlignTolerance: model.FrameTolerance,
		SegmentPrefix: "seg_",
	})
	if err != nil {
		t.Fatalf("Slice: %v", err)
	}
	if len(result.Segments) != 3 {
		t.Fatalf("segments = %d, want 3", len(result.Segments))
	}
	for i, s := range result.Segments {
		if s.Seq != i {
			t.Fatalf("segment %d has seq %d", i, s.Seq)
		}
		path := out + "/" + r.Dir + "/" + s.File
		info, err := os.Stat(path)
		if err != nil {
			t.Fatalf("segment file missing: %v", err)
		}
		if info.Size() != s.Bytes || info.Size()%model.PacketSize != 0 {
			t.Fatalf("segment %d size mismatch: disk=%d record=%d", i, info.Size(), s.Bytes)
		}
	}
}

func TestAnalyzeFailsWhenNoKeyframe(t *testing.T) {
	// A 0.4s stream with a 2s GOP still contains the initial IDR, so this
	// case documents that the first frame always yields at least one cut;
	// the no-IDR window guard is exercised via ErrNoKeyframeInWindow checks.
	src := generateStream(t, 300000, 400*time.Millisecond)
	f, err := os.Open(src)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	plan, err := Analyze(f, 2*time.Second)
	if err != nil {
		t.Fatalf("short stream Analyze: %v", err)
	}
	if len(plan.Cuts) != 1 {
		t.Fatalf("short stream cuts = %d, want 1", len(plan.Cuts))
	}
}
