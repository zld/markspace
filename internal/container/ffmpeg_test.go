package container

import (
	"os"
	"os/exec"
	"path/filepath"
	"testing"
)

// ffmpegAvailable reports whether an FFmpeg binary is on PATH. The kernel
// itself never shells out; this guard only enables an optional end-to-end
// scan of a real, standards-produced MOV when the tool happens to be
// installed (the acceptance brief lists FFmpeg for fixture material).
func ffmpegAvailable() bool {
	_, err := exec.LookPath("ffmpeg")
	return err == nil
}

// TestScanRealFFmpegMOV is skipped without FFmpeg. When present it asks
// FFmpeg to synthesize a short PCM MOV and confirms the scanner reads the
// track, timescale and a non-empty, in-bounds sample table.
func TestScanRealFFmpegMOV(t *testing.T) {
	if !ffmpegAvailable() {
		t.Skip("ffmpeg not installed; skipping real-container integration test")
	}
	dir := t.TempDir()
	mov := filepath.Join(dir, "real.mov")
	cmd := exec.Command("ffmpeg", "-y", "-hide_banner", "-loglevel", "error",
		"-f", "lavfi", "-i", "sine=frequency=440:duration=1",
		"-ac", "2", "-ar", "48000", "-c:a", "pcm_s16le",
		"-f", "mov", mov)
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Skipf("ffmpeg failed (%v): %s", err, out)
	}
	if _, err := os.Stat(mov); err != nil {
		t.Skipf("ffmpeg produced no output: %v", err)
	}

	f, err := Scan(mov)
	if err != nil {
		t.Fatalf("scan real MOV: %v", err)
	}
	tr := f.AudioTrack()
	if tr == nil {
		t.Fatal("no audio track in FFmpeg MOV")
	}
	if tr.Timescale != 48000 {
		t.Fatalf("timescale = %d, want 48000", tr.Timescale)
	}
	if len(tr.Samples) == 0 {
		t.Fatal("no samples scanned")
	}
	tl, err := tr.Timeline()
	if err != nil {
		t.Fatalf("timeline: %v", err)
	}
	if tl.Len() != len(tr.Samples) {
		t.Fatal("timeline lost samples")
	}
	// Every sample must sit inside the file and advance non-negatively.
	for i, s := range tr.Samples {
		if s.FileOffset < 0 || s.FileOffset+int64(s.Size) > f.Size {
			t.Fatalf("sample %d out of bounds", i)
		}
	}
}
