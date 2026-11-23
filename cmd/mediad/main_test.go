package main

import (
	"context"
	"strings"
	"testing"
)

func defaultFlags(t *testing.T) *demoFlags {
	t.Helper()
	f, err := parseFlags([]string{"--seed", "7", "--memory-limit", "1048576", "--slow-consumer-ms", "20"}, &strings.Builder{})
	if err != nil {
		t.Fatal(err)
	}
	return f
}

func TestDemoEmitsAllAcceptanceFields(t *testing.T) {
	var sb strings.Builder
	code, err := executeDemo(context.Background(), defaultFlags(t), &sb)
	if err != nil {
		t.Fatalf("demo failed: %v", err)
	}
	if code != exitOK {
		t.Fatalf("clean demo exit=%d", code)
	}
	out := sb.String()
	required := []string{
		"target_bps=", "actual_bps=", "bitrate_deviation_pct=", "quality_samples=",
		"keyframe_gap_p50_ms=", "corrupt_segments=", "manifest_alignment_error_ms=",
		"cache_hits=", "cache_source_rejects=", "checkpoint_resumes=",
		"peak_buffer_bytes=", "result_sha256=", "invariants=ok",
	}
	for _, field := range required {
		if !strings.Contains(out, field) {
			t.Errorf("missing acceptance field %q", field)
		}
	}
	if strings.Contains(out, "invariants=FAILED") {
		t.Fatalf("invariants failed:\n%s", out)
	}
	if !strings.Contains(out, "resume_digest_matches_full=true") {
		t.Error("resume digest does not match a full run")
	}
	if !strings.Contains(out, "slow_consumer_timed_out=true") {
		t.Error("slow-consumer path did not stop")
	}
}

func TestDemoIsDeterministic(t *testing.T) {
	var a, b strings.Builder
	if _, err := executeDemo(context.Background(), defaultFlags(t), &a); err != nil {
		t.Fatal(err)
	}
	if _, err := executeDemo(context.Background(), defaultFlags(t), &b); err != nil {
		t.Fatal(err)
	}
	if a.String() != b.String() {
		t.Fatal("two demo runs over the same seed produced different output")
	}
}

func TestStrictReportsCorruptionExit(t *testing.T) {
	f := defaultFlags(t)
	f.strict = true
	var sb strings.Builder
	code, err := executeDemo(context.Background(), f, &sb)
	if err != nil {
		t.Fatal(err)
	}
	if code != exitCorrupt {
		t.Fatalf("strict exit=%d want %d", code, exitCorrupt)
	}
}

func TestParseFlagsRejectsBadInput(t *testing.T) {
	cases := [][]string{
		{"--memory-limit", "0"},
		{"--memory-limit", "-1"},
		{"--seed", "-3"},
		{"--queue-depth", "0"},
		{"--window", "0"},
		{"--slow-consumer-ms", "-1"},
		{"extra-arg"},
		{"--no-such-flag"},
	}
	for _, args := range cases {
		if _, err := parseFlags(args, &strings.Builder{}); err == nil {
			t.Errorf("%v: expected error", args)
		}
	}
}

func TestRunRejectsUnknownCommand(t *testing.T) {
	var out, errOut strings.Builder
	if code := run([]string{"bogus"}, &out, &errOut); code != exitInput {
		t.Fatalf("code=%d", code)
	}
	if code := run(nil, &out, &errOut); code != exitInput {
		t.Fatalf("no-args code=%d", code)
	}
	if code := run([]string{"help"}, &out, &errOut); code != exitOK {
		t.Fatalf("help code=%d", code)
	}
}

func TestDemoHonoursMemoryLimit(t *testing.T) {
	f := defaultFlags(t)
	var sb strings.Builder
	code, err := executeDemo(context.Background(), f, &sb)
	if err != nil || code != exitOK {
		t.Fatalf("code=%d err=%v", code, err)
	}
	for _, line := range strings.Split(sb.String(), "\n") {
		if strings.HasPrefix(line, "peak_buffer_bytes=") {
			var peak int64
			for _, c := range strings.TrimPrefix(line, "peak_buffer_bytes=") {
				peak = peak*10 + int64(c-'0')
			}
			if peak > f.memoryLimit {
				t.Fatalf("peak %d exceeds limit %d", peak, f.memoryLimit)
			}
		}
	}
}
