package verify

import (
	"os/exec"
	"testing"
)

// ffmpegAvailable reports whether the local ffmpeg binary is on PATH. The
// numeric PSNR logic is already covered without it; this is an end-to-end check
// of the command construction and output parser against the real tool.
func ffmpegAvailable() bool {
	_, err := exec.LookPath("ffmpeg")
	return err == nil
}

func TestFFmpegRunnerPSNRIntegration(t *testing.T) {
	if !ffmpegAvailable() {
		t.Skip("ffmpeg not on PATH; skipping integration test")
	}
	dir := t.TempDir()
	ref := dir + "/ref.mp4"
	dist := dir + "/dist.mp4"
	mk := func(out, crf string) error {
		args := []string{
			"-hide_banner", "-loglevel", "error", "-y",
			"-f", "lavfi", "-i", "testsrc=duration=1:size=176x144:rate=25",
			"-pix_fmt", "yuv420p",
		}
		if crf != "" {
			args = append(args, "-crf", crf)
		}
		args = append(args, out)
		return exec.Command("ffmpeg", args...).Run()
	}
	if err := mk(ref, ""); err != nil {
		t.Skipf("ffmpeg fixture generation failed: %v", err)
	}
	if err := mk(dist, "30"); err != nil {
		t.Skipf("ffmpeg fixture generation failed: %v", err)
	}
	r := FFmpegRunner{}
	got, err := r.PSNR(ref, dist)
	if err != nil {
		t.Fatalf("PSNR integration: %v", err)
	}
	if got <= 0 || got == PSNRIdentical {
		t.Fatalf("expected finite positive PSNR for a lossy encode, got %v", got)
	}
}
