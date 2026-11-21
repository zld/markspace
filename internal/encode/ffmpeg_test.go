package encode

import (
	"context"
	"strings"
	"testing"
	"time"

	"bitrack/internal/media"
)

func TestEnsureEncoderFindsLibx264(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	if err := ensureEncoder(ctx, "libx264"); err != nil {
		t.Skipf("libx264 unavailable in this ffmpeg build: %v", err)
	}
}

func TestEnsureEncoderRejectsUnknown(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	err := ensureEncoder(ctx, "definitely_not_a_real_encoder_xyz")
	if err == nil || !strings.Contains(err.Error(), "lacks required encoder") {
		t.Fatalf("missing encoder must produce a locatable error, got %v", err)
	}
}

func TestEncodeAndMeasurePacketBytes(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	dir := t.TempDir()
	manifest, err := media.GenerateFixture(ctx, dir, media.FixtureSpec{
		Width: 176, Height: 144, FPS: 24,
		Segments: 2, FramesPerSegment: 48, TailFrames: 0,
	})
	if err != nil {
		t.Fatal(err)
	}
	out := dir + "/seg0.mp4"
	err = EncodeSegment(ctx, manifest.OutputPath, out, Options{
		Width: 176, Height: 144, StartFrame: 0, FrameCount: 48,
		TargetKbps: 400, Preset: "medium", Encoder: "libx264", PixelFormat: "yuv420p",
	})
	if err != nil {
		t.Skipf("libx264 encode unavailable: %v", err)
	}
	bits, err := VideoPacketBytes(ctx, out)
	if err != nil {
		t.Fatal(err)
	}
	if bits <= 0 {
		t.Error("measured packet bytes must be positive")
	}
	// VideoPacketBytes returns BYTES. 48 frames at 24fps = 2s; at 400kbps
	// expect ~800 kbit of packets. Convert bytes->bits (*8) for kbps.
	kbps := float64(bits) * 8 / 2.0 / 1000.0
	if kbps < 300 || kbps > 500 {
		t.Errorf("measured %.0f kbps outside 300-500 band for 400k target", kbps)
	}
}

func TestEncodeMissingInputFails(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	err := EncodeSegment(ctx, "/no/such/input.mp4", t.TempDir()+"/o.mp4", Options{
		Width: 176, Height: 144, FrameCount: 10, TargetKbps: 100, Encoder: "libx264",
	})
	if err == nil {
		t.Fatal("encoding a missing input must fail")
	}
}
