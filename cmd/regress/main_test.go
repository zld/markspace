package main

import (
	"os"
	"path/filepath"
	"testing"
)

// capture runs the CLI with args and returns its exit code and combined output.
func capture(t *testing.T, args ...string) (int, string) {
	t.Helper()
	dir := t.TempDir()
	outPath := filepath.Join(dir, "out")
	errPath := filepath.Join(dir, "err")
	out, err := os.Create(outPath)
	if err != nil {
		t.Fatal(err)
	}
	errf, err := os.Create(errPath)
	if err != nil {
		t.Fatal(err)
	}
	code := run(args, out, errf)
	out.Close()
	errf.Close()
	stdout, _ := os.ReadFile(outPath)
	stderr, _ := os.ReadFile(errPath)
	return code, string(stdout) + string(stderr)
}

func TestHelpExitsZero(t *testing.T) {
	if code, _ := capture(t, "--help"); code != 0 {
		t.Fatalf("help exit = %d", code)
	}
}

func TestUnknownCommandExitsOne(t *testing.T) {
	if code, _ := capture(t, "bogus"); code != 1 {
		t.Fatalf("unknown command exit = %d", code)
	}
}

func TestNoArgsExitsOne(t *testing.T) {
	if code, _ := capture(t); code != 1 {
		t.Fatalf("no-args exit = %d", code)
	}
}

func TestInvalidFlagsExitTwo(t *testing.T) {
	for _, args := range [][]string{
		{"demo", "--versions", "1"},
		{"demo", "--window", "2"},
		{"demo", "--buffer", "0"},
	} {
		code, out := capture(t, args...)
		if code != 2 {
			t.Fatalf("%v exit = %d, want 2\n%s", args, code, out)
		}
	}
}

func TestDemoSuccessExitsZero(t *testing.T) {
	code, out := capture(t, "demo", "--seed", "5", "--duration", "20",
		"--versions", "2", "--window", "16")
	if code != 0 {
		t.Fatalf("demo exit = %d\n%s", code, out)
	}
	for _, want := range []string{
		"rollback_status=committed", "av_sync_error_ms=", "loss_recovery_rate=",
		"latency_p95_ms=", "manifest_digest=",
	} {
		if !contains(out, want) {
			t.Fatalf("output missing %q\n%s", want, out)
		}
	}
}

func TestUnexpectedArgsExitOne(t *testing.T) {
	if code, _ := capture(t, "demo", "extra"); code != 1 {
		t.Fatalf("extra args exit = %d", code)
	}
}

func TestFlagParseErrorExitsOne(t *testing.T) {
	if code, _ := capture(t, "demo", "--duration", "not-a-number"); code != 1 {
		t.Fatalf("bad flag value exit = %d", code)
	}
}

func contains(haystack, needle string) bool {
	for i := 0; i+len(needle) <= len(haystack); i++ {
		if haystack[i:i+len(needle)] == needle {
			return true
		}
	}
	return false
}
