package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestDemoWritesFileAndRequiredFields(t *testing.T) {
	dir := t.TempDir()
	out := filepath.Join(dir, "demo.mp4")
	// runDemo prints to stdout; capture is unnecessary, just assert no error.
	if err := runDemo([]string{"--seed", "7", "--out", out,
		"--workdir", filepath.Join(dir, "work")}); err != nil {
		t.Fatalf("runDemo: %v", err)
	}
	if info, err := os.Stat(out); err != nil || info.Size() == 0 {
		t.Fatalf("demo output missing or empty: %v", err)
	}
}

func TestDemoRequiresOut(t *testing.T) {
	if err := runDemo([]string{"--seed", "1"}); err == nil {
		t.Fatal("demo without --out must fail")
	}
}

func TestVerifyRejectsMissingArgs(t *testing.T) {
	if err := runVerify([]string{}); err == nil {
		t.Fatal("verify without flags must fail")
	}
	if err := runVerify([]string{"--workdir", t.TempDir()}); err == nil {
		t.Fatal("verify without --out must fail")
	}
}

func TestVerifyHappyPath(t *testing.T) {
	dir := t.TempDir()
	out := filepath.Join(dir, "verify.mp4")
	err := runVerify([]string{"--workdir", filepath.Join(dir, "work"),
		"--out", out, "--seed", "7"})
	if err != nil {
		t.Fatalf("runVerify: %v", err)
	}
	if info, err := os.Stat(out); err != nil || info.Size() == 0 {
		t.Fatalf("verify output missing: %v", err)
	}
}

func TestJoinOrEmpty(t *testing.T) {
	if got := joinOrEmpty(nil); got != "<none>" {
		t.Fatalf("empty = %q, want <none>", got)
	}
	if got := joinOrEmpty([]string{"a", "b"}); got != "a,b" || !strings.Contains(got, "a") {
		t.Fatalf("join = %q", got)
	}
}

func TestFixtureOffsetStable(t *testing.T) {
	if got := fixtureOffset(); got <= 0 {
		t.Fatalf("fixture offset = %d, must be positive", got)
	}
}
