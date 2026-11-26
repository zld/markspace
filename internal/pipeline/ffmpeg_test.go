package pipeline

import (
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"rtx/internal/fixtures"
)

// TestFFmpegDecodesRepairedArtifacts cross-checks the pure-Go evidence against
// an independent decoder. It is skipped when ffmpeg/ffprobe are not installed,
// so the test suite stays hermetic.
func TestFFmpegDecodesRepairedArtifacts(t *testing.T) {
	ffmpeg, err := exec.LookPath("ffmpeg")
	if err != nil {
		t.Skip("ffmpeg not installed")
	}
	if _, err := exec.LookPath("ffprobe"); err != nil {
		t.Skip("ffprobe not installed")
	}
	root := genFixtures(t, fixtures.DefaultOptions())
	work := t.TempDir()
	input := filepath.Join(root, fixtures.NameRollback)
	out := filepath.Join(work, "out")
	runOK(t, input, filepath.Join(work, "cache"), filepath.Join(work, "cp.json"), out, 0)

	// Concatenate the init segment with the repaired fragments.
	joined := filepath.Join(work, "joined.mp4")
	var parts []byte
	initData, err := os.ReadFile(filepath.Join(input, "cam-a-init.mp4"))
	if err != nil {
		t.Fatal(err)
	}
	parts = append(parts, initData...)
	frags, err := filepath.Glob(filepath.Join(out, "*.m4s"))
	if err != nil {
		t.Fatal(err)
	}
	if len(frags) != 4 {
		t.Fatalf("fragments = %d, want 4", len(frags))
	}
	for i := 1; i <= len(frags); i++ {
		data, err := os.ReadFile(filepath.Join(out, "cam-a-000"+strconv.Itoa(i)+".m4s"))
		if err != nil {
			t.Fatal(err)
		}
		parts = append(parts, data...)
	}
	if err := os.WriteFile(joined, parts, 0o644); err != nil {
		t.Fatal(err)
	}

	// Decode fully: the repaired rollback must leave a contiguous timeline.
	if bout, err := exec.Command(ffmpeg, "-hide_banner", "-v", "error", "-i", joined, "-f", "null", "-").CombinedOutput(); err != nil {
		t.Fatalf("ffmpeg decode failed: %v\n%s", err, bout)
	}

	// PTS must be strictly increasing: a residual tfdt gap would show here.
	probe := exec.Command("ffprobe", "-hide_banner", "-v", "error", "-select_streams", "v",
		"-show_entries", "packet=pts_time", "-of", "csv=p=0", joined)
	outBytes, err := probe.Output()
	if err != nil {
		t.Fatalf("ffprobe failed: %v", err)
	}
	prev := -1.0
	lines := strings.Split(strings.TrimSpace(string(outBytes)), "\n")
	if len(lines) < 10 {
		t.Fatalf("only %d video packets", len(lines))
	}
	for _, line := range lines {
		v, err := strconv.ParseFloat(strings.TrimSpace(line), 64)
		if err != nil {
			continue
		}
		if v <= prev {
			t.Fatalf("non-monotonic PTS: %f after %f", v, prev)
		}
		prev = v
	}

	// Luma must match the fixture generator's 96 target.
	sig := exec.Command(ffmpeg, "-hide_banner", "-nostats", "-i", joined, "-map", "0:v",
		"-vf", "signalstats,metadata=print:key=lavfi.signalstats.YAVG", "-frames:v", "3", "-f", "null", "-")
	sigOut, _ := sig.CombinedOutput()
	if !strings.Contains(string(sigOut), "YAVG=96") {
		t.Fatalf("unexpected luma:\n%s", sigOut)
	}
}
