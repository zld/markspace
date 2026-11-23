package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func capture(t *testing.T, fn func() int) (int, string) {
	t.Helper()
	old := os.Stdout
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	os.Stdout = w
	code := fn()
	w.Close()
	os.Stdout = old
	buf := make([]byte, 0, 4096)
	tmp := make([]byte, 1024)
	for {
		n, err := r.Read(tmp)
		buf = append(buf, tmp[:n]...)
		if err != nil {
			break
		}
	}
	r.Close()
	return code, string(buf)
}

func TestDemoExitCodeAndOutput(t *testing.T) {
	dir := t.TempDir()
	code, out := capture(t, func() int {
		return run([]string{
			"demo", "--seed", "41", "--tracks", "2", "--fragments", "24",
			"--inject", "truncation,tfdt-regression,index-gap",
			"--checkpoint", filepath.Join(dir, "checkpoint.json"),
			"--frames", filepath.Join(dir, "frames.jsonl"),
			"--manifest", filepath.Join(dir, "manifest.json"),
			"--resume",
		})
	})
	if code != exitDamaged {
		t.Fatalf("exit=%d want %d (output: %s)", code, exitDamaged, out)
	}
	for _, want := range []string{
		"fragments=24", "tracks=2", "faults=3", "first_error_offset=",
		"offset_error_bytes=0", "recovered=true", "resume_ms=", "frames=", "sha256=",
		"invariant_failures=0",
	} {
		if !strings.Contains(out, want) {
			t.Fatalf("output missing %q:\n%s", want, out)
		}
	}
	// Repeating the exact same run must reproduce the digest and resume.
	code2, out2 := capture(t, func() int {
		return run([]string{
			"demo", "--seed", "41", "--tracks", "2", "--fragments", "24",
			"--inject", "truncation,tfdt-regression,index-gap",
			"--checkpoint", filepath.Join(dir, "checkpoint.json"),
			"--frames", filepath.Join(dir, "frames.jsonl"),
			"--manifest", filepath.Join(dir, "manifest.json"),
			"--resume",
		})
	})
	if code2 != exitDamaged {
		t.Fatalf("second run exit=%d", code2)
	}
	sha := func(s string) string {
		for _, line := range strings.Split(s, "\n") {
			if strings.HasPrefix(line, "sha256=") {
				return line
			}
		}
		return ""
	}
	if sha(out) != sha(out2) {
		t.Fatalf("sha256 differs between identical runs:\n%s\n%s", sha(out), sha(out2))
	}
	if !strings.Contains(out2, "resumed=true") {
		t.Fatalf("second run did not resume:\n%s", out2)
	}
}

func TestDemoCleanExitZero(t *testing.T) {
	dir := t.TempDir()
	code, out := capture(t, func() int {
		return run([]string{
			"demo", "--seed", "7", "--tracks", "2", "--fragments", "6",
			"--frames", filepath.Join(dir, "f.jsonl"),
			"--manifest", filepath.Join(dir, "m.json"),
		})
	})
	if code != exitOK {
		t.Fatalf("exit=%d want 0:\n%s", code, out)
	}
	if !strings.Contains(out, "faults=0") {
		t.Fatalf("clean input reported faults:\n%s", out)
	}
}

func TestVerifyExitCodes(t *testing.T) {
	dir := t.TempDir()
	cfgArgs := []string{
		"demo", "--seed", "41", "--tracks", "2", "--fragments", "12",
		"--inject", "tfdt-regression",
		"--frames", filepath.Join(dir, "frames.jsonl"),
		"--manifest", filepath.Join(dir, "manifest.json"),
	}
	if code, out := capture(t, func() int { return run(cfgArgs) }); code != exitDamaged {
		t.Fatalf("demo exit=%d:\n%s", code, out)
	}
	code, out := capture(t, func() int {
		return run([]string{"verify", "--manifest", filepath.Join(dir, "manifest.json"), "--frames", filepath.Join(dir, "frames.jsonl")})
	})
	if code != exitOK {
		t.Fatalf("verify exit=%d:\n%s", code, out)
	}
	for _, want := range []string{"invariant_failures=0", "manifest_sha256=", "frame_count="} {
		if !strings.Contains(out, want) {
			t.Fatalf("verify output missing %q:\n%s", want, out)
		}
	}
	// A missing manifest is an unrecoverable verification failure.
	code, _ = capture(t, func() int {
		return run([]string{"verify", "--manifest", filepath.Join(dir, "nope.json"), "--frames", filepath.Join(dir, "nope.jsonl")})
	})
	if code != exitUnrecoverable {
		t.Fatalf("missing manifest exit=%d want 1", code)
	}
}

func TestDemoRejectsBadConfiguration(t *testing.T) {
	code, _ := capture(t, func() int {
		return run([]string{"demo", "--fragments", "0"})
	})
	if code != exitUnrecoverable {
		t.Fatalf("exit=%d want 1", code)
	}
	code, _ = capture(t, func() int {
		return run([]string{"demo", "--inject", "nonsense"})
	})
	if code != exitUnrecoverable {
		t.Fatalf("unknown fault exit=%d want 1", code)
	}
}

func TestResumeRefusedExitsOne(t *testing.T) {
	dir := t.TempDir()
	cp := filepath.Join(dir, "checkpoint.json")
	if code, _ := capture(t, func() int {
		return run([]string{
			"demo", "--seed", "41", "--tracks", "2", "--fragments", "12",
			"--inject", "tfdt-regression", "--checkpoint", cp,
			"--frames", filepath.Join(dir, "a.jsonl"), "--manifest", filepath.Join(dir, "a.json"),
		})
	}); code != exitDamaged {
		t.Fatalf("setup exit=%d", code)
	}
	// Same checkpoint path, different input seed: resume must be refused.
	code, _ := capture(t, func() int {
		return run([]string{
			"demo", "--seed", "99", "--tracks", "2", "--fragments", "12",
			"--inject", "tfdt-regression", "--checkpoint", cp, "--resume",
			"--frames", filepath.Join(dir, "b.jsonl"), "--manifest", filepath.Join(dir, "b.json"),
		})
	})
	if code != exitUnrecoverable {
		t.Fatalf("refused resume exit=%d want 1", code)
	}
}
