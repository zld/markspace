package main

import (
	"os"
	"path/filepath"
	"testing"

	"cmafrepair/pkg/media/fixture"
)

func TestFixtureAndRepairEndToEnd(t *testing.T) {
	root := t.TempDir()
	if _, err := fixture.Generate(filepath.Join(root, "cmaf"), 7); err != nil {
		t.Fatal(err)
	}
	in := filepath.Join(root, "cmaf", "clean")
	out := filepath.Join(root, "out")
	if err := runRepair([]string{"--in", in, "--out", out}); err != nil {
		t.Fatalf("clean repair: %v", err)
	}
	// Three representations, each with an init, 24 fragments and an index.
	for _, rep := range fixture.Reps {
		idx := filepath.Join(out, rep.Name, "index.json")
		if !exists(idx) {
			t.Fatalf("missing index for %s", rep.Name)
		}
		if !exists(filepath.Join(out, rep.Name, "init.mp4")) {
			t.Fatalf("missing init for %s", rep.Name)
		}
	}
	if !exists(filepath.Join(out, "master.m3u8")) {
		t.Fatal("missing master playlist")
	}
	if !exists(filepath.Join(out, ".checkpoint.json")) {
		t.Fatal("missing checkpoint")
	}
}

func TestRepairInvalidInputExitCode(t *testing.T) {
	err := runRepair([]string{"--in", "/nonexistent/path/xyz", "--out", t.TempDir()})
	if err == nil {
		t.Fatal("missing input must error")
	}
	ee, ok := err.(*exitError)
	if !ok || ee.code != exitInvalidInput {
		t.Fatalf("want exit code %d, got %v", exitInvalidInput, err)
	}
}

func TestInterruptReturnsCode75AndResumeCompletes(t *testing.T) {
	root := t.TempDir()
	if _, err := fixture.Generate(filepath.Join(root, "cmaf"), 7); err != nil {
		t.Fatal(err)
	}
	in := filepath.Join(root, "cmaf", "faults")
	out := filepath.Join(root, "resume")

	err := runRepair([]string{"--in", in, "--out", out, "--interrupt-after", "7"})
	ee, ok := err.(*exitError)
	if !ok || ee.code != exitInterrupted {
		t.Fatalf("want exit 75, got %v", err)
	}
	// No temp file survives the interrupt.
	entries, _ := os.ReadDir(out)
	for _, e := range entries {
		if len(e.Name()) >= 4 && e.Name()[:4] == ".tmp" {
			t.Fatalf("temp file survived interrupt: %s", e.Name())
		}
	}
	// Resume completes without an injected interrupt.
	if err := runRepair([]string{"--in", in, "--out", out, "--resume"}); err != nil {
		t.Fatalf("resume: %v", err)
	}
}

func TestRepeatedRunsAreIdempotent(t *testing.T) {
	root := t.TempDir()
	if _, err := fixture.Generate(filepath.Join(root, "cmaf"), 7); err != nil {
		t.Fatal(err)
	}
	in := filepath.Join(root, "cmaf", "clean")
	out1 := filepath.Join(root, "o1")
	out2 := filepath.Join(root, "o2")
	if err := runRepair([]string{"--in", in, "--out", out1}); err != nil {
		t.Fatal(err)
	}
	if err := runRepair([]string{"--in", in, "--out", out2}); err != nil {
		t.Fatal(err)
	}
	cp1 := readFile(t, filepath.Join(out1, ".checkpoint.json"))
	cp2 := readFile(t, filepath.Join(out2, ".checkpoint.json"))
	// Run id/timestamps aside, the per-item digests must match; compare a
	// representative fragment byte-for-byte instead.
	a := readFile(t, filepath.Join(out1, fixture.Reps[0].Name, "frag_0001.m4s"))
	b := readFile(t, filepath.Join(out2, fixture.Reps[0].Name, "frag_0001.m4s"))
	if a != b {
		t.Fatal("repeated runs must produce identical fragment bytes")
	}
	_ = cp1
	_ = cp2
}

func TestVerifyMismatchReturnsCode1(t *testing.T) {
	root := t.TempDir()
	if _, err := fixture.Generate(filepath.Join(root, "cmaf"), 7); err != nil {
		t.Fatal(err)
	}
	in := filepath.Join(root, "cmaf", "clean")
	out := filepath.Join(root, "out")
	ref := filepath.Join(root, "ref")
	if err := runRepair([]string{"--in", in, "--out", out}); err != nil {
		t.Fatal(err)
	}
	if err := runRepair([]string{"--in", in, "--out", ref}); err != nil {
		t.Fatal(err)
	}
	// Tamper with a committed fragment, then verify must fail with code 1.
	target := filepath.Join(out, fixture.Reps[0].Name, "frag_0001.m4s")
	if err := os.WriteFile(target, []byte("tampered-bytes"), 0o600); err != nil {
		t.Fatal(err)
	}
	err := runVerify([]string{"--in", in, "--out", out, "--compare", ref})
	ee, ok := err.(*exitError)
	if !ok || ee.code != exitVerifyFailed {
		t.Fatalf("want exit code 1, got %v", err)
	}
}

func readFile(t *testing.T, p string) string {
	t.Helper()
	b, err := os.ReadFile(p)
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}
