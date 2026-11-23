package main

import (
	"path/filepath"
	"testing"
)

func TestRunNoArgsIsArgumentError(t *testing.T) {
	if code := run(nil); code != 2 {
		t.Fatalf("no args exit = %d, want 2", code)
	}
	if code := run([]string{"bogus"}); code != 2 {
		t.Fatalf("bad subcommand exit = %d, want 2", code)
	}
}

func TestRunDemoMissingOutFlag(t *testing.T) {
	if code := run([]string{"demo", "--seed", "1", "--fragments", "4",
		"--late-at", "1"}); code != 2 {
		t.Fatalf("missing --out exit = %d, want 2", code)
	}
}

func TestRunDemoLateAtOutOfRange(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "out")
	if code := run([]string{"demo", "--seed", "1", "--fragments", "3",
		"--late-at", "9", "--out", dir}); code != 2 {
		t.Fatalf("out-of-range late-at exit = %d, want 2", code)
	}
}

func TestRunDemoAndVerifyRoundTrip(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "out")
	code := run([]string{"demo", "--seed", "42", "--fragments", "8",
		"--late-at", "3", "--out", dir})
	if code != 0 {
		t.Fatalf("demo exit = %d, want 0", code)
	}
	if code := run([]string{"verify", "--dir", dir}); code != 0 {
		t.Fatalf("verify exit = %d, want 0", code)
	}
}

func TestRunVerifyMissingDir(t *testing.T) {
	if code := run([]string{"verify", "--dir",
		filepath.Join(t.TempDir(), "absent")}); code != 1 {
		t.Fatalf("missing dir exit = %d, want 1", code)
	}
	if code := run([]string{"verify"}); code != 2 {
		t.Fatalf("verify without --dir exit = %d, want 2", code)
	}
}

func TestRunHelp(t *testing.T) {
	if code := run([]string{"--help"}); code != 0 {
		t.Fatalf("help exit = %d, want 0", code)
	}
}

func TestParseIntRejectsGarbage(t *testing.T) {
	if _, err := parseInt("abc"); err == nil {
		t.Fatal("non-integer must error")
	}
}
