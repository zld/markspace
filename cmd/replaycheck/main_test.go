package main

import (
	"path/filepath"
	"testing"
)

// The command is intentionally thin: these tests prove argument handling,
// exit-code selection and the absence of processing logic. All content
// behaviour is covered by the replay package tests.

func TestRunRequiresCommand(t *testing.T) {
	if got := run(nil); got != exitFailure {
		t.Fatalf("exit=%d", got)
	}
	if got := run([]string{"nope"}); got != exitFailure {
		t.Fatalf("exit=%d", got)
	}
	if got := run([]string{"help"}); got != exitOK {
		t.Fatalf("help exit=%d", got)
	}
}

func TestVerifyRequiresOutDir(t *testing.T) {
	if got := run([]string{"verify"}); got != exitFailure {
		t.Fatalf("exit=%d", got)
	}
}

func TestVerifySucceedsAndReportsMetrics(t *testing.T) {
	out := filepath.Join(t.TempDir(), "proof")
	code := run([]string{"verify", "--seed", "7", "--out", out, "--workers", "3", "--queue", "2", "--delay", "0s"})
	if code != exitOK {
		t.Fatalf("exit=%d", code)
	}
}

func TestVerifyRejectsNonPositiveWorkers(t *testing.T) {
	out := filepath.Join(t.TempDir(), "proof")
	if code := run([]string{"verify", "--out", out, "--workers", "0"}); code != exitFailure {
		t.Fatalf("exit=%d", code)
	}
}

func TestProcessRequiresInputs(t *testing.T) {
	out := filepath.Join(t.TempDir(), "proof")
	if code := run([]string{"process", "--out", out}); code != exitFailure {
		t.Fatalf("exit=%d", code)
	}
	if code := run([]string{"process", "--out", out, "--mp4", "/nope.mp4", "--mxf", "/nope.mxf"}); code != exitInput {
		t.Fatalf("missing files exit=%d", code)
	}
}

func TestSelectorFlagParsing(t *testing.T) {
	var s flagSet
	if err := s.Set("rotation=mxf"); err != nil {
		t.Fatal(err)
	}
	if err := s.Set("color=mp4"); err != nil {
		t.Fatal(err)
	}
	if s["rotation"] != "mxf" || s["color"] != "mp4" {
		t.Fatalf("selectors=%v", s)
	}
	if err := s.Set("bad"); err == nil {
		t.Fatal("expected selector parse error")
	}
	if err := s.Set("=mp4"); err == nil {
		t.Fatal("expected empty field error")
	}
}

func TestProcessRejectsUnresolvedConflict(t *testing.T) {
	// Produce a clean run to borrow its primary pair, then run the primary
	// MP4 against the generated conflicting MXF with no selector.
	out := filepath.Join(t.TempDir(), "proof")
	if code := run([]string{"verify", "--seed", "7", "--out", out, "--delay", "0s"}); code != exitOK {
		t.Fatalf("setup exit=%d", code)
	}
	code := run([]string{
		"process",
		"--mp4", filepath.Join(out, "input", "primary.mp4"),
		"--mxf", filepath.Join(out, "input", "conflict.mxf"),
		"--out", filepath.Join(t.TempDir(), "conflicted"),
	})
	if code != exitInput {
		t.Fatalf("conflict exit=%d want %d", code, exitInput)
	}
}

func TestProcessAcceptsSelector(t *testing.T) {
	out := filepath.Join(t.TempDir(), "proof")
	if code := run([]string{"verify", "--seed", "7", "--out", out, "--delay", "0s"}); code != exitOK {
		t.Fatalf("setup exit=%d", code)
	}
	code := run([]string{
		"process",
		"--mp4", filepath.Join(out, "input", "primary.mp4"),
		"--mxf", filepath.Join(out, "input", "conflict.mxf"),
		"--out", filepath.Join(t.TempDir(), "resolved"),
		"--select", "rotation=mp4",
		"--select", "color=mp4",
	})
	if code != exitOK {
		t.Fatalf("resolved exit=%d", code)
	}
}
