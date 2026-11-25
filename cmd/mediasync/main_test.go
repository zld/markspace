package main

import "testing"

func TestRunExitCodes(t *testing.T) {
	if got := run(nil); got != 2 {
		t.Fatalf("no args exit = %d, want 2", got)
	}
	if got := run([]string{"frobnicate"}); got != 2 {
		t.Fatalf("unknown command exit = %d, want 2", got)
	}
	if got := run([]string{"demo", "--seed", "notanint", "--out", t.TempDir()}); got != 2 {
		t.Fatalf("bad seed exit = %d, want 2", got)
	}
	if got := run([]string{"demo", "--seed", "1"}); got != 2 {
		t.Fatalf("missing --out exit = %d, want 2", got)
	}
	if got := run([]string{"verify"}); got != 2 {
		t.Fatalf("verify without --dir exit = %d, want 2", got)
	}
}

func TestRunDemoVerifyRecoverLifecycle(t *testing.T) {
	out := t.TempDir()

	// Successful demo exits 0 and produces a verifiable directory.
	if got := run([]string{"demo", "--seed", "7", "--out", out}); got != 0 {
		t.Fatalf("demo success exit = %d, want 0", got)
	}
	if got := run([]string{"verify", "--dir", out}); got != 0 {
		t.Fatalf("verify exit = %d, want 0", got)
	}

	// A truncated run into a fresh directory fails with code 1, then recover
	// completes it and verify passes.
	bad := t.TempDir()
	if got := run([]string{"demo", "--case", "truncated", "--out", bad}); got != 1 {
		t.Fatalf("truncated demo exit = %d, want 1", got)
	}
	if got := run([]string{"recover", "--dir", bad}); got != 0 {
		t.Fatalf("recover exit = %d, want 0", got)
	}
	if got := run([]string{"verify", "--dir", bad}); got != 0 {
		t.Fatalf("verify after recover exit = %d, want 0", got)
	}
}

func TestRunVerifyFailsOnMissingInput(t *testing.T) {
	// A directory with no persisted fixture cannot be verified -> exit 1.
	if got := run([]string{"verify", "--dir", t.TempDir()}); got != 1 {
		t.Fatalf("verify on empty dir exit = %d, want 1", got)
	}
}
