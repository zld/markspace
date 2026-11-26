package main

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestVerifyAllScenarios drives the real CLI entry point through the full
// acceptance scenario and asserts the exit code and key transcript lines.
func TestVerifyAllScenarios(t *testing.T) {
	root := t.TempDir()
	var buf bytes.Buffer
	code := runWith(t, []string{
		"verify", "--scenario", "all", "--root", root,
		"--pre", "5s", "--post", "8s",
		"--min-width", "1280", "--min-fps", "30",
		"--max-work-bytes", "67108864", "--max-output-bytes", "16777216",
	}, &buf)
	out := buf.String()
	if code != exitOK {
		t.Fatalf("exit = %d\n%s", code, out)
	}
	for _, want := range []string{
		"round-trip parse consistency: 18/18",
		"keyframe hits 8/8",
		"slice count: 3",
		"rerun recomputation is 0",
		"envelope change reuses 3 slices",
		"event change recomputes exactly 1 slice",
		"BOX_TRUNCATED",
		"TIME_REGRESSION",
		"TIME_OVERFLOW",
		"EVENT_OUT_OF_RANGE",
		"existing manifest hash unchanged after failures",
		"rollback manifest retained",
		"resumed migration completes",
	} {
		if !strings.Contains(out, want) {
			t.Fatalf("transcript missing %q\n%s", want, out)
		}
	}
	if strings.Contains(out, "[FAIL]") {
		t.Fatalf("transcript reported a failure\n%s", out)
	}
	// The manifest must exist and reference three published assets.
	data, err := os.ReadFile(filepath.Join(root, "manifest.json"))
	if err != nil {
		t.Fatal(err)
	}
	if got := strings.Count(string(data), `"slice_id"`); got != 3 {
		t.Fatalf("manifest entries = %d", got)
	}
}

// TestVerifyIsRepeatable proves the scenario is deterministic across two roots.
func TestVerifyIsRepeatable(t *testing.T) {
	first := t.TempDir()
	second := t.TempDir()
	var a, b bytes.Buffer
	if code := runWith(t, []string{"verify", "--scenario", "normal", "--root", first}, &a); code != exitOK {
		t.Fatalf("first run exit=%d\n%s", code, a.String())
	}
	if code := runWith(t, []string{"verify", "--scenario", "normal", "--root", second}, &b); code != exitOK {
		t.Fatalf("second run exit=%d\n%s", code, b.String())
	}
	if !strings.Contains(a.String(), "manifest: sha256=") {
		t.Fatal("first run did not report a manifest hash")
	}
	ha := manifestHashOf(a.String())
	hb := manifestHashOf(b.String())
	if ha == "" || ha != hb {
		t.Fatalf("manifest hashes differ: %q vs %q", ha, hb)
	}
}

// TestArgumentErrors checks the 0/1/2 exit-code contract.
func TestArgumentErrors(t *testing.T) {
	cases := []struct {
		name string
		args []string
	}{
		{"no subcommand", nil},
		{"unknown subcommand", []string{"render"}},
		{"missing root", []string{"verify"}},
		{"bad scenario", []string{"verify", "--scenario", "nope", "--root", t.TempDir()}},
		{"bad pre", []string{"verify", "--root", t.TempDir(), "--pre", "soon"}},
		{"bad bytes", []string{"verify", "--root", t.TempDir(), "--max-work-bytes", "lots"}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if _, err := parseArgs(c.args); err == nil {
				t.Fatalf("parseArgs(%v) succeeded, want error", c.args)
			}
		})
	}
}

// TestScenarioMismatchExitsOne forces a quality-gate failure through the CLI.
func TestScenarioMismatchExitsOne(t *testing.T) {
	var buf bytes.Buffer
	code := runWith(t, []string{"verify", "--scenario", "normal", "--root", t.TempDir(), "--min-width", "3840"}, &buf)
	if code != exitMismatch {
		t.Fatalf("exit = %d, want %d\n%s", code, exitMismatch, buf.String())
	}
	if !strings.Contains(buf.String(), "QUALITY_GATE_FAILED") {
		t.Fatalf("missing quality gate code\n%s", buf.String())
	}
}

func manifestHashOf(out string) string {
	const marker = "manifest: sha256="
	i := strings.Index(out, marker)
	if i < 0 {
		return ""
	}
	rest := out[i+len(marker):]
	if j := strings.IndexAny(rest, " \n"); j >= 0 {
		return rest[:j]
	}
	return rest
}

// runWith invokes the CLI in-process with a captured reporter, restoring
// os.Args/stdout so parallel package tests stay isolated.
func runWith(t *testing.T, args []string, out *bytes.Buffer) int {
	t.Helper()
	cfg, err := parseArgs(args)
	if err != nil {
		t.Fatalf("parseArgs: %v", err)
	}
	r := newReporter(out)
	return execute(r, cfg)
}
