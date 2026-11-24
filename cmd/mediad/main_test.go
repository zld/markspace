package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"mediad/internal/app"
	"mediad/internal/sample"
	"mediad/pipeline"
)

func captureRun(t *testing.T, args ...string) (int, string) {
	t.Helper()
	// Redirect stdout/stderr by swapping the package-level writers indirectly:
	// run() writes to os.Stdout/os.Stderr, so capture via files.
	rOut, wOut, _ := os.Pipe()
	oldOut := os.Stdout
	oldErr := os.Stderr
	os.Stdout = wOut
	os.Stderr = wOut
	defer func() { os.Stdout = oldOut; os.Stderr = oldErr }()

	code := run(args)
	wOut.Close()
	buf := make([]byte, 1<<16)
	n, _ := rOut.Read(buf)
	return code, string(buf[:n])
}

func TestCLIDemoSuccess(t *testing.T) {
	out := filepath.Join(t.TempDir(), "demo")
	code, out1 := captureRun(t, "demo", "--out", out)
	if code != pipeline.ExitOK {
		t.Fatalf("exit code = %d, want 0; output=%s", code, out1)
	}
	for _, key := range []string{
		"bitrate_target_bps", "bitrate_actual_bps", "quality_errors",
		"manifest_max_delta_ms", "duplicate_count", "checkpoint_reused",
	} {
		if !strings.Contains(out1, key) {
			t.Fatalf("output missing metric %q:\n%s", key, out1)
		}
	}
	if !strings.Contains(out1, "acceptance: OK") {
		t.Fatalf("acceptance banner missing:\n%s", out1)
	}
}

func TestCLITruncateExitCodeTwo(t *testing.T) {
	out := filepath.Join(t.TempDir(), "bad")
	code, out1 := captureRun(t, "demo", "--fault", "truncate", "--out", out)
	if code != pipeline.ExitCorruptInput {
		t.Fatalf("exit code = %d, want 2; output=%s", code, out1)
	}
	if !strings.Contains(out1, "truncated_offset=") {
		t.Fatalf("truncation offset not reported:\n%s", out1)
	}
	if strings.Contains(out1, "truncated_offset=-1") {
		t.Fatalf("truncation offset must be a real offset:\n%s", out1)
	}
}

func TestCLIInterruptResumeMatchesFullRun(t *testing.T) {
	out := filepath.Join(t.TempDir(), "int")
	code, out1 := captureRun(t, "demo", "--interrupt-after", "fingerprint", "--out", out)
	if code != pipeline.ExitOK {
		t.Fatalf("exit code = %d, want 0; output=%s", code, out1)
	}
	if !strings.Contains(out1, "resume_matches_full_run=true") {
		t.Fatalf("resume equivalence not proven:\n%s", out1)
	}
	if !strings.Contains(out1, "reused=3") {
		t.Fatalf("resume must reuse the three completed nodes:\n%s", out1)
	}
}

func TestCLIRejectsUnknownFaultAndMissingCommand(t *testing.T) {
	if code, _ := captureRun(t, "demo", "--fault", "nope", "--out", t.TempDir()); code != 1 {
		t.Fatalf("unknown fault exit = %d, want 1", code)
	}
	if code, _ := captureRun(t); code != 1 {
		t.Fatalf("missing command exit = %d, want 1", code)
	}
}

func TestCLIResumeReusesCheckpointsOnSecondInvocation(t *testing.T) {
	out := filepath.Join(t.TempDir(), "resume")
	// First full run populates checkpoints under out/normal.
	if code, o := captureRun(t, "demo", "--out", out); code != 0 {
		t.Fatalf("first run failed: %s", o)
	}
	// Running the same kind again reuses every checkpoint; no node recomputes.
	rep, err := app.Run(sample.KindNormal, app.DefaultConfig(filepath.Join(out, "normal")))
	if err != nil {
		t.Fatalf("resumed run failed: %v", err)
	}
	if rep.Ran() != 0 || rep.Reused() != 4 {
		t.Fatalf("second invocation: ran=%d reused=%d, want 0 and 4", rep.Ran(), rep.Reused())
	}
}
