package main

import (
	"os"
	"path/filepath"
	"testing"
)

// cli executes the command in-process and returns its exit code.
func cli(args ...string) int { return run(args) }

func TestCLIUsageErrorsExitTwo(t *testing.T) {
	cases := [][]string{
		nil,
		{"bogus"},
		{"generate"},
		{"run"},
		{"run", "--input", "x.rtp"},
		{"verify"},
		{"run", "--input", "x.rtp", "--out", "y", "--sample-rate", "12345"},
		{"run", "--input", "x.rtp", "--out", "y", "--channels", "5"},
		{"run", "--input", "x.rtp", "--out", "y", "--delay-ms", "-1"},
		{"run", "--input", "x.rtp", "--out", "y", "--payload-channels", "3"},
		{"run", "--input", "x.rtp", "--out", "y", "--definitely-not-a-flag"},
	}
	for _, args := range cases {
		if got := cli(args...); got != 2 {
			t.Fatalf("%v -> exit %d, want 2", args, got)
		}
	}
}

func TestCLIHelpExitsZero(t *testing.T) {
	if got := cli("--help"); got != 0 {
		t.Fatalf("help -> %d", got)
	}
}

func TestCLIEndToEnd(t *testing.T) {
	dir := t.TempDir()
	fixtures := filepath.Join(dir, "fixtures")
	if got := cli("generate", "--out", fixtures); got != 0 {
		t.Fatalf("generate -> %d", got)
	}
	for _, name := range []string{"normal.rtp", "reordered.rtp", "damaged.rtp", "wrap.rtp"} {
		if _, err := os.Stat(filepath.Join(fixtures, name)); err != nil {
			t.Fatalf("%s missing: %v", name, err)
		}
	}
	out := filepath.Join(dir, "out")
	args := []string{"run", "--input", filepath.Join(fixtures, "normal.rtp"),
		"--sample-rate", "48000", "--channels", "2", "--target-lufs", "-23", "--out", out}
	if got := cli(args...); got != 0 {
		t.Fatalf("run -> %d", got)
	}
	for _, name := range []string{"normal.mp4", "normal.manifest.json", "normal.stats.json"} {
		if _, err := os.Stat(filepath.Join(out, name)); err != nil {
			t.Fatalf("%s missing: %v", name, err)
		}
	}
	if got := cli("verify", "--dir", out); got != 0 {
		t.Fatalf("verify -> %d", got)
	}
	// Running again must reproduce the same file hash.
	out2 := filepath.Join(dir, "out2")
	if got := cli(append([]string{"run", "--out", out2}, args[1:len(args)-2]...)...); got != 0 {
		t.Fatalf("second run -> %d", got)
	}
	a, err := os.ReadFile(filepath.Join(out, "normal.mp4"))
	if err != nil {
		t.Fatal(err)
	}
	b, err := os.ReadFile(filepath.Join(out2, "normal.mp4"))
	if err != nil {
		t.Fatal(err)
	}
	if string(a) != string(b) {
		t.Fatal("repeated run produced different MP4 bytes")
	}
}

func TestCLIVerifyDetectsTampering(t *testing.T) {
	dir := t.TempDir()
	fixtures := filepath.Join(dir, "fixtures")
	if got := cli("generate", "--out", fixtures); got != 0 {
		t.Fatalf("generate -> %d", got)
	}
	out := filepath.Join(dir, "out")
	if got := cli("run", "--input", filepath.Join(fixtures, "normal.rtp"), "--out", out); got != 0 {
		t.Fatalf("run -> %d", got)
	}
	path := filepath.Join(out, "normal.mp4")
	f, err := os.OpenFile(path, os.O_RDWR, 0)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := f.WriteAt([]byte{0x41, 0x41, 0x41, 0x41}, 64); err != nil {
		t.Fatal(err)
	}
	f.Close()
	if got := cli("verify", "--dir", out); got != 1 {
		t.Fatalf("tampered verify -> %d, want 1", got)
	}
	if got := cli("verify", "--dir", filepath.Join(dir, "missing")); got != 1 {
		t.Fatalf("missing dir verify -> %d, want 1", got)
	}
}

func TestCLIInputFailuresExitOne(t *testing.T) {
	dir := t.TempDir()
	bad := filepath.Join(dir, "bad.rtp")
	if err := os.WriteFile(bad, []byte("not a capture"), 0o644); err != nil {
		t.Fatal(err)
	}
	if got := cli("run", "--input", bad, "--out", filepath.Join(dir, "o")); got != 1 {
		t.Fatalf("bad capture -> %d, want 1", got)
	}
	if got := cli("run", "--input", filepath.Join(dir, "absent.rtp"), "--out", filepath.Join(dir, "o")); got != 1 {
		t.Fatalf("missing capture -> %d, want 1", got)
	}
}
