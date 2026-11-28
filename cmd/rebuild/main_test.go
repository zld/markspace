package main

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"rtp-rebuild/internal/manifest"
)

func TestParseArgs(t *testing.T) {
	cfg, err := parseArgs([]string{"--self-test", "--seed", "9", "--budget-ms", "2500", "--out", "/tmp/x"})
	if err != nil {
		t.Fatalf("parseArgs: %v", err)
	}
	if !cfg.SelfTest || cfg.Seed != 9 || cfg.BudgetMS != 2500 || cfg.OutDir != "/tmp/x" {
		t.Fatalf("cfg = %+v", cfg)
	}
	if err := cfg.validate(); err != nil {
		t.Fatalf("validate: %v", err)
	}
}

func TestParseArgsRejects(t *testing.T) {
	cases := [][]string{
		{"--bogus"},
		{"--self-test", "--budget-ms", "0"},
		{"--self-test", "--budget-ms", "-5"},
		{"--self-test", "--seed", "-1"},
		{"--self-test", "--out", ""},
		{"--self-test", "--stream", "x.rtrb"},
		{"--stream", "/definitely/not/here.rtrb"},
		{"--self-test", "extra"},
		{"--self-test", "--interrupt-after", "nope"},
	}
	for _, args := range cases {
		cfg, err := parseArgs(args)
		if err != nil {
			continue // parse error is acceptable
		}
		if err := cfg.validate(); err == nil {
			t.Fatalf("args %v should be rejected", args)
		}
	}
}

func TestSelfTestEndToEnd(t *testing.T) {
	dir := t.TempDir()
	cfg, err := parseArgs([]string{"--self-test", "--seed", "7", "--budget-ms", "10000", "--out", dir})
	if err != nil {
		t.Fatal(err)
	}
	rep, err := execute(context.Background(), cfg)
	if err != nil {
		t.Fatalf("execute: %v", err)
	}
	if !rep.passed {
		t.Fatalf("self-test failed: %+v", rep.streams)
	}
	if len(rep.streams) != 2 {
		t.Fatalf("streams = %d", len(rep.streams))
	}
	normal := rep.streams[0]
	if normal.Unique != 100 || normal.Duplicates != 0 || normal.Loss != 0 || normal.OffsetErrors != 0 {
		t.Fatalf("normal stream = %+v", normal)
	}
	faulty := rep.streams[1]
	if faulty.GapIntervals < 1 || faulty.OffsetErrors < 1 || faulty.TruncatedAt < 0 {
		t.Fatalf("fault stream = %+v", faulty)
	}
	if rep.exitCode() != exitOK {
		t.Fatalf("exit = %d", rep.exitCode())
	}

	// The published manifest must be valid and complete.
	data, err := os.ReadFile(filepath.Join(dir, "manifest.json"))
	if err != nil {
		t.Fatal(err)
	}
	m, err := manifest.Parse(data)
	if err != nil {
		t.Fatalf("manifest: %v", err)
	}
	if m.ResumeStage != "complete" {
		t.Fatalf("resume = %s", m.ResumeStage)
	}
	if len(m.Artifacts) != 2 {
		t.Fatalf("artifacts = %+v", m.Artifacts)
	}
}

func TestRepeatedDeliverySameManifest(t *testing.T) {
	dir := t.TempDir()
	args := []string{"--self-test", "--seed", "7", "--budget-ms", "10000", "--out", dir}
	cfg, _ := parseArgs(args)
	if _, err := execute(context.Background(), cfg); err != nil {
		t.Fatalf("first: %v", err)
	}
	first, err := os.ReadFile(filepath.Join(dir, "manifest.json"))
	if err != nil {
		t.Fatal(err)
	}
	cfg, _ = parseArgs(args)
	rep, err := execute(context.Background(), cfg)
	if err != nil {
		t.Fatalf("second: %v", err)
	}
	second, err := os.ReadFile(filepath.Join(dir, "manifest.json"))
	if err != nil {
		t.Fatal(err)
	}
	if string(first) != string(second) {
		t.Fatal("repeated delivery changed the manifest")
	}
	if !rep.passed {
		t.Fatalf("second run failed: %+v", rep.items)
	}
}

func TestInterruptThenResume(t *testing.T) {
	dir := t.TempDir()
	base := []string{"--self-test", "--seed", "7", "--budget-ms", "10000", "--out", dir}

	cfg, _ := parseArgs(base)
	if _, err := execute(context.Background(), cfg); err != nil {
		t.Fatalf("baseline: %v", err)
	}
	baseline, err := os.ReadFile(filepath.Join(dir, "manifest.json"))
	if err != nil {
		t.Fatal(err)
	}

	cfg, _ = parseArgs(append(base, "--interrupt-after", "mux"))
	rep, err := execute(context.Background(), cfg)
	if err != nil {
		t.Fatalf("interrupt: %v", err)
	}
	if !rep.interrupted {
		t.Fatal("run should be interrupted")
	}
	if rep.resumeStage != "validate" {
		t.Fatalf("resume stage = %s", rep.resumeStage)
	}

	cfg, _ = parseArgs(append(base, "--resume"))
	rep, err = execute(context.Background(), cfg)
	if err != nil {
		t.Fatalf("resume: %v", err)
	}
	if !rep.passed {
		t.Fatalf("resumed run failed: %+v", rep.streams)
	}
	resumed, err := os.ReadFile(filepath.Join(dir, "manifest.json"))
	if err != nil {
		t.Fatal(err)
	}
	if string(baseline) != string(resumed) {
		t.Fatal("resume did not reproduce the baseline manifest")
	}
}

func TestStreamModeUsesGivenPath(t *testing.T) {
	dir := t.TempDir()
	cfg, _ := parseArgs([]string{"--self-test", "--out", filepath.Join(dir, "gen")})
	if _, err := execute(context.Background(), cfg); err != nil {
		t.Fatalf("generate: %v", err)
	}
	normal := filepath.Join(dir, "gen", "input", "normal.rtrb")
	if _, err := os.Stat(normal); err != nil {
		t.Fatalf("fixture missing: %v", err)
	}
	out := filepath.Join(dir, "out")
	cfg, err := parseArgs([]string{"--stream", normal, "--out", out})
	if err != nil {
		t.Fatal(err)
	}
	rep, err := execute(context.Background(), cfg)
	if err != nil {
		t.Fatalf("stream mode: %v", err)
	}
	if len(rep.streams) != 1 || rep.streams[0].Unique != 100 {
		t.Fatalf("stream report = %+v", rep.streams)
	}
	var sb strings.Builder
	rep.print(&sb)
	if !strings.Contains(sb.String(), "stream.input.unique=100") {
		t.Fatalf("stream mode report:\n%s", sb.String())
	}
	if !strings.Contains(rep.manifestSHA, "") {
		t.Fatal("manifest digest missing")
	}
}

func TestReportPrint(t *testing.T) {
	dir := t.TempDir()
	cfg, _ := parseArgs([]string{"--self-test", "--out", dir})
	rep, err := execute(context.Background(), cfg)
	if err != nil {
		t.Fatalf("execute: %v", err)
	}
	var sb strings.Builder
	rep.print(&sb)
	out := sb.String()
	for _, key := range []string{
		"packets=", "unique=", "duplicates=", "loss=", "loss_rate=", "jitter=",
		"gap_intervals=", "offset_errors=", "peak_buffer_bytes=", "elapsed_ms=",
		"budget_delta_ms=", "manifest_sha256=", "artifact_sha256=", "resume_stage=",
		"item_results=", "stream.normal.truncated_offset=", "stream.fault.truncated_offset=",
		"result=PASS",
	} {
		if !strings.Contains(out, key) {
			t.Fatalf("report missing %q:\n%s", key, out)
		}
	}
}
