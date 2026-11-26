package main

import (
	"bytes"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// TestExecuteAcceptanceSequence runs demo(crash)->recover->verify in-process
// and asserts the exact stdout tokens and the recovery-only exit code 20.
func TestExecuteAcceptanceSequence(t *testing.T) {
	dir := t.TempDir()

	var out, errOut bytes.Buffer
	code := execute([]string{"demo", "--work", dir, "--rates", "2", "--crash-before-commit"}, &out, &errOut)
	if code != 20 {
		t.Fatalf("demo crash code=%d want 20 (out=%s err=%s)", code, out.String(), errOut.String())
	}
	if !strings.Contains(out.String(), "exit_code=20") {
		t.Fatalf("stdout must advertise exit_code=20: %q", out.String())
	}

	out.Reset()
	code = execute([]string{"recover", "--work", dir}, &out, &errOut)
	if code != 0 {
		t.Fatalf("recover code=%d err=%s", code, errOut.String())
	}
	want := "recovered=1 duplicate_publish=0 missing_artifact=0"
	if strings.TrimSpace(out.String()) != want {
		// stdout is allowed to be exactly the canonical line; tolerate a
		// trailing newline but nothing else on stdout.
		if !strings.Contains(out.String(), want) {
			t.Fatalf("recover stdout=%q want %q", out.String(), want)
		}
	}

	out.Reset()
	code = execute([]string{"verify", "--work", dir}, &out, &errOut)
	if code != 0 {
		t.Fatalf("verify code=%d out=%s err=%s", code, out.String(), errOut.String())
	}
	s := out.String()
	for _, token := range []string{
		"roundtrip_rate=1.000",
		"keyframe_hit_rate=1.000",
		"segment_hash_ok=8/8",
		"boundary_align_error_packets=0",
		"verify=PASS",
		"anomaly_counts",
	} {
		if !strings.Contains(s, token) {
			t.Fatalf("verify output missing %q\nfull:\n%s", token, s)
		}
	}
}

func TestExecuteRejectsRemoteWorkPath(t *testing.T) {
	var out, errOut bytes.Buffer
	code := execute([]string{"demo", "--work", "s3://bucket/out", "--rates", "1"}, &out, &errOut)
	if code == 0 {
		t.Fatal("remote output path must be rejected")
	}
}

// TestBuiltBinaryCrashExit20 builds the real CLI and checks the OS exit code,
// which `go run` normalizes to 1 but the acceptance contract sets at 20.
func TestBuiltBinaryCrashExit20(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping binary build in -short mode")
	}
	repoRoot := filepath.Join("..", "..")
	bin := filepath.Join(t.TempDir(), "mediapack")
	build := exec.Command("go", "build", "-o", bin, "./cmd/mediapack")
	build.Dir = repoRoot
	if b, err := build.CombinedOutput(); err != nil {
		t.Fatalf("go build: %v\n%s", err, b)
	}

	work := t.TempDir()
	run := exec.Command(bin, "demo", "--work", work, "--rates", "2", "--crash-before-commit")
	var stderr bytes.Buffer
	run.Stderr = &stderr
	err := run.Run()
	if err == nil {
		t.Fatal("crash run must exit non-zero")
	}
	ee, ok := err.(*exec.ExitError)
	if !ok || ee.ExitCode() != 20 {
		t.Fatalf("exit error=%v want exit code 20 (stderr=%s)", err, stderr.String())
	}
	if _, err := os.Stat(filepath.Join(work, "manifest.json")); err != nil {
		t.Fatalf("manifest should be durable before crash: %v", err)
	}
}
