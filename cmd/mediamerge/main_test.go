package main

import "testing"

func TestParseInt(t *testing.T) {
	cases := map[string]int{"0": 0, "2": 2, "123": 123}
	for in, want := range cases {
		got, err := parseInt(in)
		if err != nil || got != want {
			t.Fatalf("parseInt(%q) = %d, %v", in, got, err)
		}
	}
	for _, bad := range []string{"", "-1", "1.5", "abc"} {
		if _, err := parseInt(bad); err == nil {
			t.Fatalf("parseInt(%q) must fail", bad)
		}
	}
}

func TestParseFlags(t *testing.T) {
	f, err := parseFlags([]string{"--case", "normal", "--out", "/tmp/x"})
	if err != nil {
		t.Fatal(err)
	}
	if f.caseName != "normal" || f.out != "/tmp/x" || f.policy != "mark-gaps" {
		t.Fatalf("defaults wrong: %+v", f)
	}
	if _, err := parseFlags([]string{"--case", "x"}); err == nil {
		t.Fatal("missing --out must be rejected")
	}
	if _, err := parseFlags([]string{"--out", "/tmp/x", "--policy", "nope"}); err == nil {
		t.Fatal("invalid policy must be rejected")
	}
	if _, err := parseFlags([]string{"--bogus"}); err == nil {
		t.Fatal("unknown flag must be rejected")
	}
}

func TestRunDispatch(t *testing.T) {
	if code := run(nil); code != 2 {
		t.Fatalf("empty args exit = %d, want 2", code)
	}
	if code := run([]string{"frobnicate"}); code != 2 {
		t.Fatalf("unknown subcommand exit = %d, want 2", code)
	}
	if code := run([]string{"help"}); code != 0 {
		t.Fatalf("help exit = %d, want 0", code)
	}
	// verify with a missing required flag is a rule rejection (2).
	if code := run([]string{"verify", "--out", t.TempDir()}); code != 2 {
		t.Fatalf("verify without case exit = %d, want 2", code)
	}
}

func TestRunVerifyConflictExitTwo(t *testing.T) {
	dir := t.TempDir()
	code := run([]string{"verify", "--case", "conflict", "--policy", "strict", "--out", dir})
	if code != 2 {
		t.Fatalf("strict conflict exit = %d, want 2", code)
	}
}

func TestRunVerifyDamagedExitZero(t *testing.T) {
	dir := t.TempDir()
	code := run([]string{"verify", "--case", "damaged", "--policy", "mark-gaps", "--out", dir})
	if code != 0 {
		t.Fatalf("damaged under mark-gaps exit = %d, want 0", code)
	}
}

func TestRunRecoveryAndResume(t *testing.T) {
	dir := t.TempDir()
	if code := run([]string{"verify", "--case", "recovery", "--interrupt-after", "2",
		"--out", dir}); code != 0 {
		t.Fatalf("interrupted verify exit = %d, want 0", code)
	}
	if code := run([]string{"resume", "--out", dir}); code != 0 {
		t.Fatalf("resume exit = %d, want 0", code)
	}
}
