package main

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"tracecheck/internal/index"
)

// runCLI invokes the command in-process and returns exit code, stdout, stderr.
func runCLI(t *testing.T, args ...string) (int, string, string) {
	t.Helper()
	var out, errb bytes.Buffer
	code := run(args, &out, &errb)
	return code, out.String(), errb.String()
}

func TestDemoRunsAndResumes(t *testing.T) {
	work := t.TempDir()
	code, stdout, stderr := runCLI(t, "demo", "--work", work, "--chunk", "2")
	if code != 0 {
		t.Fatalf("first demo exit=%d stderr=%s", code, stderr)
	}
	for _, want := range []string{
		"roundtrip_rate=1.000",
		"keyframe_hit_rate=1.000",
		"segment_sha256_0=",
		"error_offset=",
		"resume_reprocessed_chunks=0",
		"mp4_structure=valid",
	} {
		if !strings.Contains(stdout, want) {
			t.Errorf("demo output missing %q", want)
		}
	}
	// Every segment hash must be a 64-char hex string.
	for _, line := range strings.Split(stdout, "\n") {
		if strings.HasPrefix(line, "segment_sha256_") {
			hash := line[strings.Index(line, "=")+1:]
			if len(hash) != 64 {
				t.Errorf("segment hash %q is not 64 hex chars", hash)
			}
		}
	}

	// Resume must keep index digest and segment hashes identical.
	hashBefore := extractSegments(stdout)
	digestBefore := extractLine(stdout, "index_digest=")
	code, stdout2, stderr2 := runCLI(t, "demo", "--work", work, "--chunk", "2", "--resume")
	if code != 0 {
		t.Fatalf("resume exit=%d stderr=%s", code, stderr2)
	}
	if got := extractLine(stdout2, "resume_reprocessed_chunks="); got != "resume_reprocessed_chunks=0" {
		t.Errorf("resume reprocessed chunks: %s", got)
	}
	if extractLine(stdout2, "index_digest=") != digestBefore {
		t.Error("resume changed the index digest")
	}
	if !equalStrings(hashBefore, extractSegments(stdout2)) {
		t.Error("resume changed segment hashes")
	}
}

func TestDemoTruncatedBlockReportsOffset(t *testing.T) {
	work := t.TempDir()
	code, stdout, _ := runCLI(t, "demo", "--work", work, "--chunk", "2")
	if code != 0 {
		// A demo invocation is successful even though it reports damaged inputs.
		t.Fatalf("demo exit=%d", code)
	}
	if !strings.Contains(stdout, "error_code=LENGTH_OUT_OF_BOUNDS") {
		t.Error("truncated-block error code missing")
	}
	if !strings.Contains(stdout, "error_code=PARENT_BOUNDARY_CONFLICT") {
		t.Error("boundary-conflict error code missing")
	}
	// Offsets must be nonzero for both reported samples.
	offsets := 0
	for _, line := range strings.Split(stdout, "\n") {
		if strings.HasPrefix(line, "error_offset=") {
			offsets++
			if line == "error_offset=0" || line == "error_offset=-1" {
				t.Errorf("unexpected error offset: %s", line)
			}
		}
	}
	if offsets < 2 {
		t.Errorf("expected at least two error offsets, got %d", offsets)
	}
}

func TestDemoInvalidChunk(t *testing.T) {
	code, _, _ := runCLI(t, "demo", "--work", t.TempDir(), "--chunk", "0")
	if code != 1 {
		t.Fatalf("chunk 0 exit=%d, want 1", code)
	}
}

func TestCheckDamagedExitsTwo(t *testing.T) {
	work := t.TempDir()
	if code, _, stderr := runCLI(t, "demo", "--work", work); code != 0 {
		t.Fatalf("seed demo exit=%d: %s", code, stderr)
	}
	code, stdout, _ := runCLI(t, "check", filepath.Join(work, "truncated-block.webm"))
	if code != 2 {
		t.Fatalf("check damaged exit=%d, want 2", code)
	}
	if !strings.Contains(stdout, "error_code=LENGTH_OUT_OF_BOUNDS") {
		t.Errorf("check output: %s", stdout)
	}
}

func TestCheckHealthyExitsZero(t *testing.T) {
	work := t.TempDir()
	if code, _, _ := runCLI(t, "demo", "--work", work); code != 0 {
		t.Fatal("seed demo failed")
	}
	code, stdout, _ := runCLI(t, "check", filepath.Join(work, "healthy.webm"))
	if code != 0 {
		t.Fatalf("check healthy exit=%d, want 0", code)
	}
	if !strings.Contains(stdout, "status=ok") {
		t.Errorf("check output: %s", stdout)
	}
}

func TestCheckMissingFileExitsOne(t *testing.T) {
	code, _, _ := runCLI(t, "check", filepath.Join(t.TempDir(), "missing.webm"))
	if code != 1 {
		t.Fatalf("check missing exit=%d, want 1", code)
	}
}

func TestResumeWithCorruptCheckpoint(t *testing.T) {
	work := t.TempDir()
	if code, _, _ := runCLI(t, "demo", "--work", work, "--chunk", "2"); code != 0 {
		t.Fatal("seed demo failed")
	}
	cpPath := index.CheckpointPath(work, "healthy.webm")
	if err := os.WriteFile(cpPath, []byte("{corrupt"), 0o644); err != nil {
		t.Fatal(err)
	}
	code, _, stderr := runCLI(t, "demo", "--work", work, "--chunk", "2", "--resume")
	if code != 2 {
		t.Fatalf("corrupt checkpoint resume exit=%d, want 2; stderr=%s", code, stderr)
	}
}

func extractLine(text, prefix string) string {
	for _, line := range strings.Split(text, "\n") {
		if strings.HasPrefix(line, prefix) {
			return line
		}
	}
	return ""
}

func extractSegments(text string) []string {
	var out []string
	for _, line := range strings.Split(text, "\n") {
		if strings.HasPrefix(line, "segment_sha256_") {
			out = append(out, line)
		}
	}
	return out
}

func equalStrings(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}
