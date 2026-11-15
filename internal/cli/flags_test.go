package cli

import (
	"bytes"
	"strings"
	"testing"
)

func baseArgs(extra ...string) []string {
	args := []string{
		"run",
		"--input", "in.srt", "--output", "out.vtt",
		"--input-format", "srt", "--output-format", "vtt",
		"--fps", "24000/1001", "--overlap", "clip",
	}
	return append(args, extra...)
}

func TestParseOK(t *testing.T) {
	var stderr bytes.Buffer
	opts, err := Parse(baseArgs("--jsonl", "--max-delta-ms", "42"), &stderr)
	if err != nil {
		t.Fatal(err)
	}
	if !opts.JSONL || opts.MaxDeltaMS != 42 {
		t.Errorf("flag values wrong: %+v", opts)
	}
	if opts.FPS.Num != 24000 || opts.FPS.Den != 1001 {
		t.Errorf("fps = %d/%d", opts.FPS.Num, opts.FPS.Den)
	}
}

func TestParseUsageErrors(t *testing.T) {
	var stderr bytes.Buffer
	cases := [][]string{
		{"run", "--input", "x", "--output", "y"}, // missing formats
		baseArgs("--input-format", "mkv"),        // bad format
		baseArgs("--fps", "abc"),                 // bad fps
		baseArgs("--fps", "0/1"),                 // non-positive fps
		baseArgs("--overlap", "merge"),           // bad strategy
		baseArgs("--max-delta-ms", "-1"),         // negative threshold
	}
	for _, args := range cases {
		if _, err := Parse(args, &stderr); !IsUsage(err) {
			t.Errorf("args %v: expected usage error, got %v", args, err)
		}
	}
}

func TestDemoRequiresFormatsNotPaths(t *testing.T) {
	var stderr bytes.Buffer
	opts, err := Parse([]string{
		"demo", "--input-format", "srt", "--output-format", "ass",
		"--fps", "25", "--overlap", "keep",
	}, &stderr)
	if err != nil {
		t.Fatal(err)
	}
	if !opts.Demo {
		t.Error("demo flag not set")
	}
}

func TestRunRequiresPaths(t *testing.T) {
	var stderr bytes.Buffer
	_, err := Parse([]string{
		"run", "--input-format", "srt", "--output-format", "vtt",
	}, &stderr)
	if !IsUsage(err) {
		t.Errorf("expected usage error for missing paths, got %v", err)
	}
	if !strings.Contains(err.Error(), "--input") {
		t.Errorf("error should name the missing flag: %v", err)
	}
}
