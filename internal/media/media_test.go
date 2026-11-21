package media

import (
	"context"
	"strings"
	"testing"
	"time"
)

func TestGenerateFixtureDeterministic(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	spec := FixtureSpec{
		Width: 176, Height: 144, FPS: 24,
		Segments: 3, FramesPerSegment: 48, TailFrames: 12,
	}
	dir1, dir2 := t.TempDir(), t.TempDir()
	m1, err := GenerateFixture(ctx, dir1, spec)
	if err != nil {
		t.Fatalf("fixture 1: %v", err)
	}
	m2, err := GenerateFixture(ctx, dir2, spec)
	if err != nil {
		t.Fatalf("fixture 2: %v", err)
	}
	sum1, err := FileSHA256Pub(m1.OutputPath)
	if err != nil {
		t.Fatal(err)
	}
	sum2, err := FileSHA256Pub(m2.OutputPath)
	if err != nil {
		t.Fatal(err)
	}
	if sum1 != sum2 {
		t.Error("identical specs must produce byte-identical fixtures")
	}
	wantFrames := 3*48 + 12
	if m1.TotalFrames != wantFrames {
		t.Errorf("total frames = %d, want %d", m1.TotalFrames, wantFrames)
	}
}

func TestProbeAcceptsGeneratedFixture(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	dir := t.TempDir()
	m, err := GenerateFixture(ctx, dir, FixtureSpec{
		Width: 176, Height: 144, FPS: 24,
		Segments: 2, FramesPerSegment: 48, TailFrames: 8,
	})
	if err != nil {
		t.Fatal(err)
	}
	info, err := Probe(ctx, m.OutputPath)
	if err != nil {
		t.Fatalf("probe: %v", err)
	}
	if !info.HasVideo || !info.IsCFR {
		t.Errorf("generated fixture should be CFR video: %+v", info)
	}
	if info.Width != 176 || info.Height != 144 {
		t.Errorf("dimensions = %dx%d", info.Width, info.Height)
	}
	if info.FieldOrder != "progressive" {
		t.Errorf("field order = %s, want progressive", info.FieldOrder)
	}
	if info.NbFrames != 2*48+8 {
		t.Errorf("nb_frames = %d", info.NbFrames)
	}
}

func TestProbeRejectsMissingFile(t *testing.T) {
	ctx := context.Background()
	if _, err := Probe(ctx, "/nonexistent/video.mp4"); err == nil {
		t.Fatal("probe on missing file must error")
	}
}

func TestDecodeFramesAligned(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	dir := t.TempDir()
	m, err := GenerateFixture(ctx, dir, FixtureSpec{
		Width: 160, Height: 120, FPS: 24,
		Segments: 2, FramesPerSegment: 48, TailFrames: 0,
	})
	if err != nil {
		t.Fatal(err)
	}
	frames, err := DecodeFrames(ctx, m.OutputPath, 160, 120, 10, 20)
	if err != nil {
		t.Fatalf("decode: %v", err)
	}
	if len(frames) != 20 {
		t.Errorf("decoded %d frames, want 20", len(frames))
	}
	want := 160 * 120 * 3 / 2
	for i, f := range frames {
		if len(f) != want {
			t.Errorf("frame %d size = %d, want %d", i, len(f), want)
		}
	}
}

func TestFixtureSpecValidation(t *testing.T) {
	cases := []FixtureSpec{
		{Width: 177, Height: 144, FPS: 24, Segments: 1, FramesPerSegment: 10, TailFrames: 0},
		{Width: 176, Height: 144, FPS: 0, Segments: 1, FramesPerSegment: 10, TailFrames: 0},
		{Width: 176, Height: 144, FPS: 24, Segments: 0, FramesPerSegment: 10, TailFrames: 0},
		{Width: 176, Height: 144, FPS: 24, Segments: 1, FramesPerSegment: 10, TailFrames: 10},
	}
	for i, spec := range cases {
		if err := validateFixtureSpec(spec); err == nil {
			t.Errorf("case %d: invalid spec accepted: %+v", i, spec)
		}
	}
}

func TestParseRate(t *testing.T) {
	cases := map[string]float64{
		"24/1":       24,
		"30000/1001": 29.97002997002997,
		"25":         25,
	}
	for in, want := range cases {
		got, err := parseRate(in)
		if err != nil {
			t.Errorf("parseRate(%q): %v", in, err)
			continue
		}
		if got-want > 0.001 || got-want < -0.001 {
			t.Errorf("parseRate(%q) = %v, want %v", in, got, want)
		}
	}
	if _, err := parseRate("0/0"); err == nil || !strings.Contains(err.Error(), "invalid") {
		t.Errorf("0/0 must be rejected, got %v", err)
	}
}
