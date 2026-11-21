package quality

import (
	"context"
	"os/exec"
	"path/filepath"
	"testing"
	"time"
)

// makeClip writes a solid-color clip of the given duration.
func makeClip(t *testing.T, path string, dur string) {
	t.Helper()
	out, err := exec.Command("ffmpeg", "-v", "error", "-y",
		"-f", "lavfi", "-i", "testsrc2=size=64x64:rate=10:duration="+dur,
		"-c:v", "libx264", "-pix_fmt", "yuv420p", path).CombinedOutput()
	if err != nil {
		t.Fatalf("make clip: %v: %s", err, out)
	}
}

// A frame-count mismatch must be reported, must not hang, and must
// not produce a fabricated mean.
func TestRangeFrameMismatch(t *testing.T) {
	dir := t.TempDir()
	ref := filepath.Join(dir, "ref.mp4")
	dec := filepath.Join(dir, "dec.mp4")
	makeClip(t, ref, "2")
	makeClip(t, dec, "1")
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	st, err := Range(ctx, ref, dec, 0, 2, 10, 64, 64, 25)
	if err != nil {
		t.Fatal(err)
	}
	if !st.FrameMismatch {
		t.Fatal("expected FrameMismatch")
	}
	if st.MeanDB != 0 {
		t.Fatalf("mean must not be fabricated on mismatch, got %.2f", st.MeanDB)
	}
}

// Equal-length clips must produce a finite mean and no mismatch.
func TestRangeMatch(t *testing.T) {
	dir := t.TempDir()
	ref := filepath.Join(dir, "ref.mp4")
	makeClip(t, ref, "1")
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	st, err := Range(ctx, ref, ref, 0, 1, 10, 64, 64, 25)
	if err != nil {
		t.Fatal(err)
	}
	if st.FrameMismatch {
		t.Fatal("unexpected mismatch")
	}
	if st.Frames != 10 {
		t.Fatalf("expected 10 frames, got %d", st.Frames)
	}
	if st.MeanDB < 90 { // identical input saturates at the cap
		t.Fatalf("expected near-cap PSNR, got %.2f", st.MeanDB)
	}
}
