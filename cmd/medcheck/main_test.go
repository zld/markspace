package main

import "testing"

func TestRunExitCodes(t *testing.T) {
	// Argument errors must map to exit code 2.
	if code := run(nil); code != exitArgError {
		t.Fatalf("no args: exit=%d want %d", code, exitArgError)
	}
	if code := run([]string{"--seed", "-1", "--selftest"}); code != exitArgError {
		t.Fatalf("negative seed: exit=%d want %d", code, exitArgError)
	}
	if code := run([]string{"--no-such-flag"}); code != exitArgError {
		t.Fatalf("unknown flag: exit=%d want %d", code, exitArgError)
	}
}

func TestRunSelfTestSucceeds(t *testing.T) {
	dir := t.TempDir()
	code := run([]string{"--selftest", "--seed", "42", "--out", dir})
	if code != exitOK {
		t.Fatalf("selftest exit=%d want %d", code, exitOK)
	}
}
