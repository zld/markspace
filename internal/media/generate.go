package media

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
)

// GenerateSample writes a deterministic sample video to path: four
// equal phases of differing motion and texture, encoded with a keyframe
// every second so the segment planner can cut freely. All filter
// sources are deterministic (fixed seeds, no wall-clock input).
func GenerateSample(ctx context.Context, path string, durationSec, fps float64) error {
	if durationSec < 4 {
		return fmt.Errorf("sample duration %.1fs too short; need >= 4s for four phases", durationSec)
	}
	phase := durationSec / 4
	size := "640x360"
	rate := strconv.FormatFloat(fps, 'f', 0, 64)
	dur := strconv.FormatFloat(phase, 'f', 3, 64)
	sources := []string{
		"testsrc2=size=" + size + ":rate=" + rate,
		"mandelbrot=size=" + size + ":rate=" + rate + ":maxiter=80",
		"smptebars=size=" + size + ":rate=" + rate,
		"gradients=size=" + size + ":rate=" + rate + ":seed=42:duration=" + dur,
	}
	args := []string{"-v", "error", "-nostdin", "-y"}
	for i, src := range sources {
		args = append(args, "-f", "lavfi")
		if i < 3 {
			args = append(args, "-t", dur)
		}
		args = append(args, "-i", src)
	}
	// Concat the four phases, then add a fixed-seed spatial noise floor
	// so PSNR is finite and complexity differs across phases.
	filter := "concat=n=4:v=1:a=0[v];[v]noise=alls=8:allf=u,format=yuv420p[out]"
	args = append(args,
		"-filter_complex", filter,
		"-map", "[out]",
		"-c:v", "libx264", "-preset", "medium", "-crf", "18",
		"-g", rate, "-keyint_min", rate,
		"-movflags", "+faststart", path)
	out, err := exec.CommandContext(ctx, "ffmpeg", args...).CombinedOutput()
	if err != nil {
		return fmt.Errorf("generate sample: %w: %s", err, tail(out))
	}
	return nil
}

// GenerateFixtures writes abnormal-input fixtures into dir: a corrupt
// file ffprobe cannot read and a variable-frame-rate clip. Both are
// derived locally; nothing is downloaded.
func GenerateFixtures(ctx context.Context, dir string) error {
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	corrupt := filepath.Join(dir, "corrupt.mp4")
	b := make([]byte, 4096)
	for i := range b {
		b[i] = byte(i * 31)
	}
	if err := os.WriteFile(corrupt, b, 0o644); err != nil {
		return err
	}
	vfr := filepath.Join(dir, "vfr.mp4")
	out, err := exec.CommandContext(ctx, "ffmpeg",
		"-v", "error", "-nostdin", "-y",
		"-f", "lavfi", "-t", "2", "-i", "testsrc2=size=320x180:rate=15",
		"-f", "lavfi", "-t", "2", "-i", "testsrc2=size=320x180:rate=30",
		"-filter_complex", "concat=n=2:v=1:a=0[v]",
		"-map", "[v]", "-c:v", "libx264", "-g", "15", vfr).CombinedOutput()
	if err != nil {
		return fmt.Errorf("generate vfr fixture: %w: %s", err, tail(out))
	}
	return nil
}

func tail(b []byte) string {
	const n = 400
	if len(b) > n {
		return string(b[len(b)-n:])
	}
	return string(b)
}
