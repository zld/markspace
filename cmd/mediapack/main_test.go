package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"mediapack/internal/fault"
	"mediapack/internal/fixture"
)

func TestExitCodeMapping(t *testing.T) {
	cases := []struct {
		err  error
		want int
	}{
		{fault.New(fault.KindCheckpointCorrupt, 0, "bad"), 3},
		{fault.New(fault.KindTruncated, 5, "cut").AsRecoverable(), 2},
		{fault.New(fault.KindLengthOutOfRange, 9, "long"), 2},
		{fault.New(fault.KindInputChanged, 1, "changed"), 2},
	}
	for _, c := range cases {
		if got := ExitCode(c.err); got != c.want {
			t.Errorf("ExitCode(%v)=%d want %d", c.err, got, c.want)
		}
	}
}

func TestDemoGeneratesAllSamples(t *testing.T) {
	dir := t.TempDir()
	if err := runDemo([]string{"--seed", "7", "--out", dir}); err != nil {
		t.Fatalf("demo must succeed (it only generates samples): %v", err)
	}
	want := []string{"valid.webm", "unknown-length.webm", "truncated.webm",
		"oversize.webm", "regression.webm"}
	for _, name := range want {
		if _, err := os.Stat(filepath.Join(dir, name)); err != nil {
			t.Fatalf("demo did not generate %s: %v", name, err)
		}
	}
}

func TestRunTruncatedReturnsFault(t *testing.T) {
	dir := t.TempDir()
	spec := fixture.Spec{
		Seed: 7, Codecs: []string{"V_VP9", "A_OPUS"},
		Clusters: 3, FramesPerTrack: 2, PayloadMin: 24, PayloadMax: 48,
	}
	raw, _ := fixture.Build(spec, fixture.CorruptTruncate)
	input := filepath.Join(dir, "truncated.webm")
	if err := os.WriteFile(input, raw, 0o644); err != nil {
		t.Fatal(err)
	}
	err := runCmd([]string{"--input", input, "--index", filepath.Join(dir, "idx")})
	if !fault.Is(err, fault.KindTruncated) {
		t.Fatalf("expected truncation fault, got %v", err)
	}
	if ExitCode(err) != 2 {
		t.Fatal("truncated input must map to exit code 2")
	}
}

func TestRunCorruptIndexExitThree(t *testing.T) {
	dir := t.TempDir()
	spec := fixture.Spec{
		Seed: 7, Codecs: []string{"V_VP9"},
		Clusters: 2, FramesPerTrack: 2, PayloadMin: 20, PayloadMax: 30,
	}
	raw, _ := fixture.Build(spec, fixture.CorruptNone)
	input := filepath.Join(dir, "in.webm")
	if err := os.WriteFile(input, raw, 0o644); err != nil {
		t.Fatal(err)
	}
	// Seed a valid checkpoint by running once with an interruption.
	if err := runCmd([]string{
		"--input", input, "--index", filepath.Join(dir, "idx"),
		"--interrupt-after", "2",
	}); err != nil {
		t.Fatal(err)
	}
	err := runCmd([]string{
		"--input", input, "--index", filepath.Join(dir, "idx"),
		"--corrupt-index",
	})
	fe := fault.Of(err)
	if fe == nil || fe.Kind != fault.KindCheckpointCorrupt {
		t.Fatalf("expected corrupt-index fault, got %v", err)
	}
	if ExitCode(err) != 3 || fe.Offset < 0 {
		t.Fatalf("corrupt index must be exit 3 with an offset, got code %d fe %v",
			ExitCode(err), fe)
	}
	if !strings.Contains(err.Error(), "checkpoint offset") {
		t.Fatalf("error must name the checkpoint offset: %v", err)
	}
}

func TestRunResumeAndDeterminism(t *testing.T) {
	dir := t.TempDir()
	spec := fixture.Spec{
		Seed: 7, Codecs: []string{"V_VP9", "A_OPUS"},
		Clusters: 3, FramesPerTrack: 2, PayloadMin: 24, PayloadMax: 48,
	}
	raw, _ := fixture.Build(spec, fixture.CorruptNone)
	input := filepath.Join(dir, "in.webm")
	if err := os.WriteFile(input, raw, 0o644); err != nil {
		t.Fatal(err)
	}
	// Interrupted first run.
	if err := runCmd([]string{
		"--input", input, "--index", filepath.Join(dir, "idx"),
		"--interrupt-after", "3",
	}); err != nil {
		t.Fatal(err)
	}
	// Resume should honor the checkpoint without error.
	if err := runCmd([]string{
		"--input", input, "--index", filepath.Join(dir, "idx"),
	}); err != nil {
		t.Fatalf("resume failed: %v", err)
	}
	// Two independent clean runs produce identical digests.
	digest := func(idx string) string {
		old := os.Stdout
		r, w, _ := os.Pipe()
		os.Stdout = w
		_ = runCmd([]string{
			"--input", input, "--index", filepath.Join(dir, idx),
		})
		_ = w.Close()
		os.Stdout = old
		var sb strings.Builder
		buf := make([]byte, 4096)
		for {
			n, rerr := r.Read(buf)
			if n > 0 {
				sb.Write(buf[:n])
			}
			if rerr != nil {
				break
			}
		}
		for _, line := range strings.Split(sb.String(), "\n") {
			if strings.HasPrefix(line, "payload_sha256=") {
				return line
			}
		}
		return ""
	}
	d1, d2 := digest("i1"), digest("i2")
	if d1 == "" || d1 != d2 {
		t.Fatalf("repeated runs must yield identical payload SHA-256: %q vs %q", d1, d2)
	}
}
