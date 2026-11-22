package main

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// buildBinary compiles the CLI once per test binary run.
func buildBinary(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	bin := filepath.Join(dir, "mediatimeline")
	cmd := exec.Command("go", "build", "-o", bin, ".")
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("go build: %v\n%s", err, out)
	}
	return bin
}

func runBinary(t *testing.T, bin string, args ...string) (string, int) {
	t.Helper()
	cmd := exec.Command(bin, args...)
	out, err := cmd.CombinedOutput()
	code := 0
	if err != nil {
		if ee, ok := err.(*exec.ExitError); ok {
			code = ee.ExitCode()
		} else {
			t.Fatalf("run: %v", err)
		}
	}
	return string(out), code
}

func TestDemoAcceptance(t *testing.T) {
	if testing.Short() {
		t.Skip("builds a binary")
	}
	bin := buildBinary(t)
	out, code := runBinary(t, bin, "demo", "--seed", "42", "--faults", "wrap,loss,color-conflict",
		"--ladder", "3", "--segment-ms", "2000")
	if code != 0 {
		t.Fatalf("acceptance demo exit = %d\n%s", code, out)
	}
	for _, want := range []string{
		"roundtrip_match=", "keyframe_hit_rate=", "segment_hash_match=",
		"recovered_layers=2/3", "boundary_errors=0", "result=PASS",
	} {
		if !strings.Contains(out, want) {
			t.Fatalf("output missing %q:\n%s", want, out)
		}
	}
	// The hash metric is exact; the consistency metrics are above 0.99.
	if !strings.Contains(out, "segment_hash_match=1.000") {
		t.Fatalf("segment hash must be exactly 1.000:\n%s", out)
	}
}

func TestDemoCleanRunIsExact(t *testing.T) {
	if testing.Short() {
		t.Skip("builds a binary")
	}
	bin := buildBinary(t)
	out, code := runBinary(t, bin, "demo", "--seed", "7", "--ladder", "3", "--segment-ms", "2000")
	if code != 0 {
		t.Fatalf("clean run exit = %d\n%s", code, out)
	}
	for _, want := range []string{"roundtrip_match=1.000", "keyframe_hit_rate=1.000", "segment_hash_match=1.000"} {
		if !strings.Contains(out, want) {
			t.Fatalf("clean run should be exact, missing %q:\n%s", want, out)
		}
	}
}

func TestDemoExitCodes(t *testing.T) {
	if testing.Short() {
		t.Skip("builds a binary")
	}
	bin := buildBinary(t)
	cases := []struct {
		name string
		args []string
		want int
	}{
		{"unrecoverable backward clock", []string{"demo", "--faults", "backward"}, 2},
		{"bad ladder", []string{"demo", "--ladder", "0"}, 1},
		{"unknown fault", []string{"demo", "--faults", "nope"}, 1},
		{"unknown command", []string{"wat"}, 1},
		{"corrupt single layer", []string{"demo", "--faults", "corrupt-layer", "--ladder", "1"}, 2},
	}
	for _, c := range cases {
		out, code := runBinary(t, bin, c.args...)
		if code != c.want {
			t.Fatalf("%s: exit = %d, want %d\n%s", c.name, code, c.want, out)
		}
	}
}

func TestDemoJSONOutput(t *testing.T) {
	if testing.Short() {
		t.Skip("builds a binary")
	}
	bin := buildBinary(t)
	out, code := runBinary(t, bin, "demo", "--json", "--seed", "42", "--faults", "wrap,loss,color-conflict")
	if code != 0 {
		t.Fatalf("json demo exit = %d\n%s", code, out)
	}
	if !strings.Contains(out, "\"recovered_layers\": 2") || !strings.Contains(out, "\"exit\": 0") {
		t.Fatalf("unexpected json:\n%s", out)
	}
}

func TestMain(m *testing.M) {
	os.Exit(m.Run())
}
