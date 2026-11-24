package main

import (
	"testing"
)

func TestCLIExitCodesForInterruptResumeAndCorruption(t *testing.T) {
	wd := t.TempDir()

	if code := run([]string{"demo", "--seed", "17", "--workdir", wd, "--interrupt-after", "index"}); code != 3 {
		t.Fatalf("interrupt code=%d want3", code)
	}
	if code := run([]string{"resume", "--workdir", wd}); code != 0 {
		t.Fatalf("resume code=%d want0", code)
	}
	if code := run([]string{"demo", "--seed", "17", "--workdir", wd, "--inject", "truncate"}); code != 2 {
		t.Fatalf("truncate code=%d want2", code)
	}
}

func TestCLIInvalidArgumentsExitTwo(t *testing.T) {
	if code := run([]string{"unknown-command"}); code != 2 {
		t.Fatalf("unknown command code=%d want2", code)
	}
	if code := run([]string{"demo", "--interrupt-after", "verify"}); code != 2 {
		t.Fatalf("invalid interrupt code=%d want2", code)
	}
}
