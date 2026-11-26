package fixture

import (
	"os"
	"os/exec"
	"testing"
)

func ffmpegPresent() bool {
	_, err := exec.LookPath("ffmpeg")
	return err == nil
}

func TestGenerateBaseIntegration(t *testing.T) {
	if !ffmpegPresent() {
		t.Skip("ffmpeg not on PATH")
	}
	dir := t.TempDir()
	b, err := GenerateBase(LocalFFmpeg{}, dir, Options{
		DurationSec: 2, TargetKbps: 400, Width: 160, Height: 120,
	})
	if err != nil {
		t.Fatalf("generate base: %v", err)
	}
	for _, p := range []string{b.RefPath, b.MP4Path, b.VTTPath, b.ChapterPath, b.ID3Path, b.RTPPath} {
		fi, err := os.Stat(p)
		if err != nil || fi.Size() == 0 {
			t.Fatalf("artifact missing or empty: %s (%v)", p, err)
		}
	}
	// The fragmented MP4 must actually contain moof boxes.
	data, _ := os.ReadFile(b.MP4Path)
	if !contains(data, "moof") {
		t.Fatal("generated MP4 is not fragmented (no moof)")
	}
}

func contains(haystack []byte, needle string) bool {
	for i := 0; i+len(needle) <= len(haystack); i++ {
		if string(haystack[i:i+len(needle)]) == needle {
			return true
		}
	}
	return false
}
