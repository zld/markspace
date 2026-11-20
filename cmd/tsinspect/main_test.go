package main

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func runCLI(t *testing.T, args ...string) (int, string, string) {
	t.Helper()
	var out, errBuf bytes.Buffer
	code := run(args, &out, &errBuf)
	return code, out.String(), errBuf.String()
}

func TestGenerateInspectSliceFlow(t *testing.T) {
	dir := t.TempDir()
	tsPath := filepath.Join(dir, "demo.ts")

	code, _, stderr := runCLI(t, "generate", "--out", tsPath, "--scenario", "updates-loss")
	if code != 0 {
		t.Fatalf("generate exit %d: %s", code, stderr)
	}

	code, stdout, stderr := runCLI(t, "inspect", "--input", tsPath, "--json")
	if code != 0 {
		t.Fatalf("inspect exit %d: %s", code, stderr)
	}
	var summary map[string]any
	if err := json.Unmarshal([]byte(stdout), &summary); err != nil {
		t.Fatalf("inspect output is not JSON: %v", err)
	}
	for _, key := range []string{"pat_version_changes", "pmt_version_changes", "program_to_pmt_pid", "streams"} {
		if _, ok := summary[key]; !ok {
			t.Fatalf("json missing %q", key)
		}
	}
	p2p, ok := summary["program_to_pmt_pid"].(map[string]any)
	if !ok || len(p2p) < 2 {
		t.Fatalf("program_to_pmt_pid = %v", summary["program_to_pmt_pid"])
	}
	if v, _ := summary["continuity_errors"].(float64); v <= 0 {
		t.Fatalf("continuity_errors = %v", summary["continuity_errors"])
	}

	outPath := filepath.Join(dir, "program-1.ts")
	code, _, stderr = runCLI(t, "slice", "--input", tsPath, "--program", "1", "--out", outPath)
	if code != 0 {
		t.Fatalf("slice exit %d: %s", code, stderr)
	}
	info, err := os.Stat(outPath)
	if err != nil {
		t.Fatal(err)
	}
	if info.Size() == 0 || info.Size()%188 != 0 {
		t.Fatalf("slice size %d is not a multiple of 188", info.Size())
	}
}

func TestExitCodes(t *testing.T) {
	dir := t.TempDir()
	tsPath := filepath.Join(dir, "base.ts")
	if code, _, stderr := runCLI(t, "generate", "--out", tsPath, "--scenario", "baseline"); code != 0 {
		t.Fatalf("generate: %s", stderr)
	}

	cases := []struct {
		name string
		args []string
		want int
	}{
		{"no args", nil, 2},
		{"unknown command", []string{"wat"}, 2},
		{"inspect missing input flag", []string{"inspect"}, 2},
		{"inspect bad flag value", []string{"inspect", "--offset", "x", "--input", tsPath}, 2},
		{"generate unknown scenario", []string{"generate", "--out", filepath.Join(dir, "x.ts"), "--scenario", "nope"}, 2},
		{"slice bad program", []string{"slice", "--input", tsPath, "--program", "zz", "--out", filepath.Join(dir, "x.ts")}, 2},
		{"inspect missing file", []string{"inspect", "--input", filepath.Join(dir, "nope.ts")}, 1},
		{"slice unknown program", []string{"slice", "--input", tsPath, "--program", "42", "--out", filepath.Join(dir, "x.ts")}, 1},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			code, _, _ := runCLI(t, tc.args...)
			if code != tc.want {
				t.Fatalf("exit = %d, want %d", code, tc.want)
			}
		})
	}
}

func TestErrorsGoToStderr(t *testing.T) {
	code, stdout, stderr := runCLI(t, "inspect", "--input", "/definitely/not/here.ts")
	if code != 1 {
		t.Fatalf("exit = %d", code)
	}
	if stdout != "" {
		t.Fatalf("unexpected stdout: %q", stdout)
	}
	if !strings.Contains(stderr, "tsinspect:") {
		t.Fatalf("stderr = %q", stderr)
	}
}

func TestSliceFailureLeavesNoOutput(t *testing.T) {
	dir := t.TempDir()
	tsPath := filepath.Join(dir, "base.ts")
	if code, _, _ := runCLI(t, "generate", "--out", tsPath, "--scenario", "baseline"); code != 0 {
		t.Fatal("generate failed")
	}
	outPath := filepath.Join(dir, "out.ts")
	code, _, _ := runCLI(t, "slice", "--input", tsPath, "--program", "77", "--out", outPath)
	if code != 1 {
		t.Fatalf("exit = %d", code)
	}
	if _, err := os.Stat(outPath); !os.IsNotExist(err) {
		t.Fatal("failed slice left output")
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	for _, e := range entries {
		if strings.HasPrefix(e.Name(), ".tsinspect") {
			t.Fatalf("temporary file left behind: %s", e.Name())
		}
	}
}

func TestInspectOffset(t *testing.T) {
	dir := t.TempDir()
	rawPath := filepath.Join(dir, "raw.ts")
	if code, _, _ := runCLI(t, "generate", "--out", rawPath, "--scenario", "baseline"); code != 0 {
		t.Fatal("generate failed")
	}
	raw, err := os.ReadFile(rawPath)
	if err != nil {
		t.Fatal(err)
	}
	paddedPath := filepath.Join(dir, "padded.ts")
	if err := os.WriteFile(paddedPath, append([]byte("JUNK"), raw...), 0o644); err != nil {
		t.Fatal(err)
	}
	code, padded, _ := runCLI(t, "inspect", "--input", paddedPath, "--offset", "4", "--json")
	if code != 0 {
		t.Fatalf("inspect offset exit %d", code)
	}
	_, plain, _ := runCLI(t, "inspect", "--input", rawPath, "--json")
	var a, b map[string]any
	if err := json.Unmarshal([]byte(padded), &a); err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal([]byte(plain), &b); err != nil {
		t.Fatal(err)
	}
	for _, key := range []string{"packets", "pat_version_changes", "pmt_version_changes", "continuity_errors"} {
		if a[key] != b[key] {
			t.Fatalf("%s differs: %v vs %v", key, a[key], b[key])
		}
	}
	if a["prefix_offset_bytes"].(float64) != 4 {
		t.Fatalf("prefix_offset_bytes = %v", a["prefix_offset_bytes"])
	}
}
