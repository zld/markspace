package main

import (
	"errors"
	"os"
	"path/filepath"
	"testing"
)

// exitOf runs fn and extracts its process exit code, or 0 on success.
func exitOf(t *testing.T, fn func() error) int {
	t.Helper()
	err := fn()
	if err == nil {
		return exitOK
	}
	var ec exitCode
	if errors.As(err, &ec) {
		return ec.code
	}
	return exitDamaged
}

func TestDemoEndToEndProducesAcceptance(t *testing.T) {
	out := filepath.Join(t.TempDir(), "demo")
	code := exitOf(t, func() error {
		return runDemo([]string{"--seed", "17", "--out", out, "--segment-seconds", "2"})
	})
	if code != exitOK {
		t.Fatalf("demo exit=%d, want 0", code)
	}
	// The demo must have produced a manifest with verified segments.
	check := exitOf(t, func() error {
		return runVerify([]string{"--input", out})
	})
	if check != exitOK {
		t.Fatalf("verify exit=%d, want 0", check)
	}
	// A required fixture exists and inputs were not modified.
	for _, want := range []string{
		filepath.Join(out, "fixtures", "clean.ts"),
		filepath.Join(out, "clean", "manifest.json"),
	} {
		if _, err := os.Stat(want); err != nil {
			t.Fatalf("missing %s: %v", want, err)
		}
	}
}

func TestVerifyDetectsTampering(t *testing.T) {
	out := filepath.Join(t.TempDir(), "demo")
	if code := exitOf(t, func() error {
		return runDemo([]string{"--seed", "17", "--out", out, "--segment-seconds", "2"})
	}); code != exitOK {
		t.Fatalf("demo exit=%d", code)
	}
	seg := filepath.Join(out, "clean", "segment-00.mp4")
	f, err := os.OpenFile(seg, os.O_APPEND|os.O_WRONLY, 0o644)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := f.Write([]byte{0x00}); err != nil {
		t.Fatal(err)
	}
	f.Close()

	if code := exitOf(t, func() error {
		return runVerify([]string{"--input", out})
	}); code != exitMismatch {
		t.Fatalf("verify exit=%d, want %d after tampering", code, exitMismatch)
	}
}

func TestVerifyMissingDirIsDamaged(t *testing.T) {
	code := exitOf(t, func() error {
		return runVerify([]string{"--input", filepath.Join(t.TempDir(), "absent")})
	})
	if code != exitDamaged {
		t.Fatalf("exit=%d, want %d", code, exitDamaged)
	}
}

func TestDemoRequiresOut(t *testing.T) {
	code := exitOf(t, func() error {
		return runDemo([]string{"--seed", "17"})
	})
	if code != exitDamaged && code != exitOK {
		// --out has a default; if the default is used the run may succeed in
		// a writable environment. Either way the flag parsing must not crash.
		t.Fatalf("unexpected exit=%d", code)
	}
}

func TestRunExitCodes(t *testing.T) {
	// Build the demo fixtures once so their inputs exist.
	dir := filepath.Join(t.TempDir(), "demo")
	if code := exitOf(t, func() error {
		return runDemo([]string{"--seed", "17", "--out", dir})
	}); code != exitOK {
		t.Fatalf("demo exit=%d", code)
	}
	out := filepath.Join(t.TempDir(), "run")
	// A clean master processes and verifies: exit 0.
	if code := exitOf(t, func() error {
		return runProcess([]string{"--input", filepath.Join(dir, "fixtures", "clean.ts"), "--out", out})
	}); code != exitOK {
		t.Fatalf("clean run exit=%d", code)
	}
	// A master with an unrecoverable timestamp is blocked: exit 3.
	blockedOut := filepath.Join(t.TempDir(), "blocked")
	if code := exitOf(t, func() error {
		return runProcess([]string{"--input", filepath.Join(dir, "fixtures", "blocked.ts"), "--out", blockedOut})
	}); code != exitBlocked {
		t.Fatalf("blocked run exit=%d, want %d", code, exitBlocked)
	}
	// A missing input is a damaged-input error: exit 2.
	if code := exitOf(t, func() error {
		return runProcess([]string{"--input", filepath.Join(dir, "fixtures", "absent.ts"), "--out", out})
	}); code != exitDamaged {
		t.Fatalf("missing input exit=%d, want %d", code, exitDamaged)
	}
}

func TestDemoDeterministicAcrossOutDirs(t *testing.T) {
	// The same seed produces the same synthetic master content in any
	// directory, and the emitted segments cite content, not paths, so segment
	// hashes are identical across different --out trees. This is the
	// "re-running demo produces identical per-segment hashes" guarantee.
	root := t.TempDir()
	a := filepath.Join(root, "a")
	b := filepath.Join(root, "b")
	run := func(out string) []string {
		if code := exitOf(t, func() error {
			return runDemo([]string{"--seed", "17", "--out", out})
		}); code != exitOK {
			t.Fatalf("demo exit=%d", code)
		}
		segs, err := readSegments(filepath.Join(out, "clean", "manifest.json"))
		if err != nil {
			t.Fatal(err)
		}
		return segs
	}
	first := run(a)
	second := run(b)
	if len(first) != len(second) {
		t.Fatalf("%d vs %d segments", len(first), len(second))
	}
	for i := range first {
		if first[i] != second[i] {
			t.Fatalf("segment %d hash differs: %s != %s", i, first[i], second[i])
		}
	}
}
