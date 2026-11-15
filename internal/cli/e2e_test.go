package cli_test

import (
	"encoding/json"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"testing"

	"transcode/internal/cli"
)

func moduleRoot(t *testing.T) string {
	t.Helper()
	_, file, _, _ := runtime.Caller(0)
	// <root>/internal/cli/e2e_test.go
	return filepath.Dir(filepath.Dir(filepath.Dir(file)))
}

func goBuild(t *testing.T, root, out, pkg string) {
	t.Helper()
	cmd := exec.Command("go", "build", "-o", out, pkg)
	cmd.Dir = root
	if b, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("build %s: %v\n%s", pkg, err, b)
	}
}

// TestEndToEndDemo builds both binaries and drives the exact acceptance
// command shape, asserting the report fields, deviation and repeatability.
func TestEndToEndDemo(t *testing.T) {
	root := moduleRoot(t)
	ws := t.TempDir()
	binDir := filepath.Join(ws, "bin")
	if err := os.MkdirAll(binDir, 0o755); err != nil {
		t.Fatal(err)
	}
	enc := filepath.Join(binDir, "stubencoder")
	tool := filepath.Join(binDir, "transcode")
	goBuild(t, root, enc, "./cmd/stubencoder")
	goBuild(t, root, tool, "./cmd/transcode")

	cp := filepath.Join(ws, "job.json")
	encoderArg := enc + " --in {in} --out {out} --target-kbps {kbps} --duration-sec {duration}"
	run := func() (map[string]any, int) {
		cmd := exec.Command(tool, "demo",
			"--duration", "120", "--segment-seconds", "10",
			"--target-kbps", "2400", "--workers", "3",
			"--checkpoint", cp, "--encoder", encoderArg, "--json")
		out, err := cmd.CombinedOutput()
		code := 0
		if err != nil {
			var exitErr *exec.ExitError
			if !errors.As(err, &exitErr) {
				t.Fatalf("run failed to start: %v\n%s", err, out)
			}
			code = exitErr.ExitCode()
		}
		var report map[string]any
		if jerr := json.Unmarshal(out, &report); jerr != nil {
			t.Fatalf("invalid JSON output (code %d): %v\n%s", code, jerr, out)
		}
		return report, code
	}

	first, code := run()
	if code != cli.ExitOK {
		t.Fatalf("first run exit = %d, want 0", code)
	}
	if int(first["segments"].(float64)) != 12 {
		t.Fatalf("segments = %v, want 12", first["segments"])
	}
	if mathAbsDev(first) > 5.0 {
		t.Fatalf("deviation exceeds 5%%: %v", first["deviation_pct"])
	}
	mean := first["psnr_mean_db"].(float64)
	if mean < 30 || mean > 45 {
		t.Fatalf("psnr_mean_db %.2f outside 30-45", mean)
	}
	if len(first["segment_psnr_db"].([]any)) != 12 {
		t.Fatal("expected 12 segment PSNR values")
	}
	if len(first["segment_psnr_delta_db"].([]any)) != 11 {
		t.Fatal("expected 11 adjacent delta values")
	}

	// Second run resumes and reports identical statistics.
	second, code := run()
	if code != cli.ExitOK {
		t.Fatalf("resume run exit = %d", code)
	}
	if second["psnr_mean_db"] != first["psnr_mean_db"] ||
		second["actual_kbps"] != first["actual_kbps"] {
		t.Fatal("repeated run produced different statistics")
	}
	recovery := second["recovery"].(map[string]any)
	if recovery["resumed"] != true {
		t.Fatal("second run must resume from the checkpoint")
	}
}

func mathAbsDev(r map[string]any) float64 {
	v := r["deviation_pct"].(float64)
	if v < 0 {
		return -v
	}
	return v
}
