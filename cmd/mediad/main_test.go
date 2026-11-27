package main

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"mediad/internal/fixture"
	"mediad/internal/model"
)

// buildCLI compiles the current package once for black-box subprocess tests.
func buildCLI(t *testing.T) string {
	t.Helper()
	bin := filepath.Join(t.TempDir(), "mediad")
	cmd := exec.Command("go", "build", "-o", bin, ".")
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("build: %v\n%s", err, out)
	}
	return bin
}

func run(t *testing.T, bin string, args ...string) (string, int) {
	cmd := exec.Command(bin, args...)
	out, err := cmd.CombinedOutput()
	code := 0
	if err != nil {
		if ee, ok := err.(*exec.ExitError); ok {
			code = ee.ExitCode()
		} else {
			t.Fatal(err)
		}
	}
	return string(out), code
}

func TestCLIFullAcceptance(t *testing.T) {
	bin := buildCLI(t)
	outDir := filepath.Join(t.TempDir(), "run")

	out, code := run(t, bin, "demo", "--fixture", "generated", "--out", outDir,
		"--interrupt-after", "stage:normalize")
	if code != 0 || !strings.Contains(out, "interrupted") {
		t.Fatalf("interrupt demo: code=%d out=%s", code, out)
	}
	out, code = run(t, bin, "resume", "--out", outDir)
	if code != 0 || !strings.Contains(out, "target_bitrate_bps") {
		t.Fatalf("resume: code=%d out=%s", code, out)
	}
	for _, key := range []string{"target_bitrate_bps", "actual_bitrate_bps",
		"bitrate_deviation_pct", "quality_metric", "manifest_alignment_error_ns"} {
		if !strings.Contains(out, key) {
			t.Fatalf("report missing %s", key)
		}
	}
	out, code = run(t, bin, "verify", "--out", outDir)
	if code != 0 || !strings.Contains(out, "verify OK") {
		t.Fatalf("verify: code=%d out=%s", code, out)
	}
}

func TestCLIErrorExitCodes(t *testing.T) {
	bin := buildCLI(t)
	cases := []struct {
		args      []string
		wantCode  int
		wantCode2 model.Code
	}{
		{[]string{"demo", "--fixture", "generated", "--out", filepath.Join(t.TempDir(), "x"),
			"--scenario", fixture.ScenBadOpus}, 2, model.ErrOpusTOC},
		{[]string{"demo", "--fixture", "generated", "--out", filepath.Join(t.TempDir(), "x"),
			"--scenario", fixture.ScenTruncAAC}, 2, model.ErrAACHeader},
		{[]string{"selfcheck", "graph-cycle"}, 2, model.ErrGraphCycle},
		{[]string{"selfcheck", "time-overflow"}, 2, model.ErrTimeOverflow},
		{[]string{"demo", "--fixture", "generated"}, 1, model.ErrUsage},
		{[]string{"bogus"}, 1, model.ErrUsage},
	}
	for _, tc := range cases {
		out, code := run(t, bin, tc.args...)
		if code != tc.wantCode || !strings.Contains(out, string(tc.wantCode2)) {
			t.Fatalf("args=%v want code=%d code2=%s, got code=%d out=%s",
				tc.args, tc.wantCode, tc.wantCode2, code, out)
		}
	}
}

func TestCLIDuplicateAndMissing(t *testing.T) {
	bin := buildCLI(t)
	dupDir := filepath.Join(t.TempDir(), "dup")
	out, code := run(t, bin, "demo", "--fixture", "generated", "--out", dupDir,
		"--scenario", fixture.ScenDuplicate)
	if code != 0 || !strings.Contains(out, "duplicates=1") {
		t.Fatalf("duplicate: %d %s", code, out)
	}
	missDir := filepath.Join(t.TempDir(), "miss")
	out, code = run(t, bin, "demo", "--fixture", "generated", "--out", missDir,
		"--scenario", fixture.ScenMissing)
	if code != 0 || !strings.Contains(out, "gaps=1") {
		t.Fatalf("missing: %d %s", code, out)
	}
}

func TestMain(m *testing.M) {
	os.Exit(m.Run())
}
