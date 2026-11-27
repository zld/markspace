package archive

import (
	"os"
	"os/exec"
	"path/filepath"
	"testing"

	"mediacore/internal/clock"
	"mediacore/internal/fault"
)

// buildFragment asks the local FFmpeg for a tiny MP4 with one track. The test
// is skipped when FFmpeg 7 is not installed.
func buildFragment(t *testing.T) string {
	t.Helper()
	ff := "/opt/homebrew/opt/ffmpeg@7/bin/ffmpeg"
	if _, err := os.Stat(ff); err != nil {
		t.Skip("FFmpeg 7 not installed")
	}
	dir := t.TempDir()
	out := filepath.Join(dir, "frag.mp4")
	cmd := exec.Command(ff, "-hide_banner", "-loglevel", "error", "-nostdin", "-y",
		"-f", "lavfi", "-i", "testsrc2=size=64x64:rate=10:duration=1",
		"-c:v", "libx264", "-preset", "ultrafast", "-pix_fmt", "yuv420p",
		"-movflags", "+faststart", out)
	if err := cmd.Run(); err != nil {
		t.Skipf("cannot build fragment: %v", err)
	}
	return out
}

func TestValidateFragmentAcceptsExactTiming(t *testing.T) {
	path := buildFragment(t)
	timing, err := ValidateFragment(path, clock.Second, 10*clock.Millisecond)
	if err != nil {
		t.Fatal(err)
	}
	if timing.MovieTimescale == 0 || len(timing.Tracks) == 0 {
		t.Fatalf("timing = %+v", timing)
	}
	if timing.Tracks[0].Handler != "vide" {
		t.Fatalf("handler = %q", timing.Tracks[0].Handler)
	}
}

func TestValidateFragmentRejectsWrongDuration(t *testing.T) {
	path := buildFragment(t)
	_, err := ValidateFragment(path, 5*clock.Second, 10*clock.Millisecond)
	f, ok := fault.As(err)
	if !ok || f.Code != fault.CodeArchiveUnverified {
		t.Fatalf("err = %v", err)
	}
}

func TestValidateFragmentRejectsNonMP4(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "bad.mp4")
	if err := os.WriteFile(path, []byte("definitely not mp4"), 0o644); err != nil {
		t.Fatal(err)
	}
	_, err := ValidateFragment(path, clock.Second, 10*clock.Millisecond)
	if _, ok := fault.As(err); !ok {
		t.Fatalf("err = %v", err)
	}
}

func TestValidateFragmentRequiresPositiveExpected(t *testing.T) {
	_, err := ValidateFragment("/nonexistent", 0, clock.Millisecond)
	if err == nil {
		t.Fatal("expected error")
	}
}
