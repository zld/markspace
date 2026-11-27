package media

import (
	"os"
	"strings"
	"testing"

	"mediacore/internal/fault"
)

func requireFFmpeg(t *testing.T) *Tool {
	t.Helper()
	tool, err := Locate()
	if err != nil {
		t.Skipf("FFmpeg 7 unavailable: %v", err)
	}
	return tool
}

func TestLocateReportsVersion(t *testing.T) {
	tool := requireFFmpeg(t)
	if !strings.Contains(tool.Version, "ffmpeg version") {
		t.Fatalf("version = %q", tool.Version)
	}
	if _, err := os.Stat(tool.FFprobe); err != nil {
		t.Fatalf("ffprobe missing: %v", err)
	}
}

func TestGenerateIsDeterministic(t *testing.T) {
	tool := requireFFmpeg(t)
	dir := t.TempDir()
	syn := Synthetic{Width: 64, Height: 64, FPS: 10, Seconds: 1, Seed: 17}
	ref1, wav1, err := tool.Generate(dir, syn)
	if err != nil {
		t.Fatal(err)
	}
	first, _ := os.ReadFile(ref1)
	firstWav, _ := os.ReadFile(wav1)
	ref2, wav2, err := tool.Generate(dir, syn)
	if err != nil {
		t.Fatal(err)
	}
	second, _ := os.ReadFile(ref2)
	secondWav, _ := os.ReadFile(wav2)
	if string(first) != string(second) || string(firstWav) != string(secondWav) {
		t.Fatal("identical inputs produced different media")
	}
	if len(first) != 64*64*3/2*10 {
		t.Fatalf("reference size = %d", len(first))
	}
}

func TestGenerateRejectsInvalid(t *testing.T) {
	tool := requireFFmpeg(t)
	if _, _, err := tool.Generate(t.TempDir(), Synthetic{Width: 0}); err == nil {
		t.Fatal("invalid description must fail")
	}
}

func TestLocateReportsActionableError(t *testing.T) {
	t.Setenv("MEDIACORE_FFMPEG", "/nonexistent/ffmpeg")
	t.Setenv("PATH", "/nonexistent")
	_, err := Locate()
	if err == nil {
		t.Skip("an ffmpeg was still found on a hardcoded path")
	}
	f, ok := fault.As(err)
	if !ok || f.Code != fault.CodeFFmpegUnavailable || !strings.Contains(f.Message, "brew install") {
		t.Fatalf("err = %v", err)
	}
}
