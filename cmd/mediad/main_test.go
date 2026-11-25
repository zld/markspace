package main

import (
	"io"
	"os"
	"strings"
	"testing"
)

func TestRunDemoExitZeroWithRejections(t *testing.T) {
	// Per-segment rejections must still yield exit code 0.
	code := run([]string{"demo", "--workers", "2", "--queue", "2",
		"--budget-ms", "500", "--max-bytes", "4096"})
	if code != exitOK {
		t.Fatalf("demo exit = %d, want 0 even with rejected segments", code)
	}
}

func TestRunUnknownCommandExitTwo(t *testing.T) {
	if code := run([]string{"frobnicate"}); code != exitArgs {
		t.Fatalf("unknown command exit = %d, want 2", code)
	}
	if code := run(nil); code != exitArgs {
		t.Fatalf("no args exit = %d, want 2", code)
	}
}

func TestRunBadFlagsExitTwo(t *testing.T) {
	cases := [][]string{
		{"demo", "--workers", "0"},
		{"demo", "--queue", "-1"},
		{"demo", "--budget-ms", "0"},
		{"demo", "--max-bytes", "0"},
		{"demo", "--workers"}, // missing value
	}
	for _, c := range cases {
		if code := run(c); code != exitArgs {
			t.Fatalf("args %v exit = %d, want 2", c, code)
		}
	}
}

func TestHelpExitZero(t *testing.T) {
	for _, c := range [][]string{{"help"}, {"--help"}, {"-h"}} {
		if code := run(c); code != exitOK {
			t.Fatalf("%v exit = %d, want 0", c, code)
		}
	}
}

func TestCancelCommandExitsZeroAndReports(t *testing.T) {
	code := run([]string{"cancel", "--workers", "1", "--queue", "2"})
	if code != exitOK {
		t.Fatalf("cancel exit = %d, want 0", code)
	}
}

func TestDemoEmitsRequiredJSONLFields(t *testing.T) {
	// Redirect stdout through an os.Pipe so we can assert on the rendered
	// report without touching the production run() signature.
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	old := os.Stdout
	os.Stdout = w
	code := run([]string{"demo", "--workers", "2", "--queue", "2",
		"--budget-ms", "500", "--max-bytes", "4096"})
	os.Stdout = old
	if err := w.Close(); err != nil {
		t.Fatal(err)
	}
	out, err := io.ReadAll(r)
	if err != nil {
		t.Fatal(err)
	}
	if code != exitOK {
		t.Fatalf("demo exit = %d", code)
	}
	text := string(out)
	for _, field := range []string{
		"samples_total", "damage_ranges", "quality_pass", "budget_ms",
		"budget_delta_ms", "bytes_estimated", "bytes_limit", "decision",
	} {
		if !strings.Contains(text, field) {
			t.Errorf("JSONL output missing field %q", field)
		}
	}
	if !strings.Contains(text, `"kind":"summary"`) {
		t.Error("summary line missing")
	}
}
