package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

func baseArgs(out string) []string {
	return []string{
		"--fixture", "valid", "--tracks", "2",
		"--bandwidth-kbps", "800,420", "--window", "4", "--out", out,
	}
}

func TestCLIRunValidExitZero(t *testing.T) {
	out := filepath.Join(t.TempDir(), "frames")
	code := run(baseArgs(out))
	if code != exitOK {
		t.Fatalf("valid run exit = %d, want 0", code)
	}
	if _, err := os.Stat(filepath.Join(out, "frames.json")); err != nil {
		t.Fatalf("frames.json not written: %v", err)
	}
	if _, err := os.Stat(filepath.Join(out, "run-state.json")); err != nil {
		t.Fatalf("run state not written: %v", err)
	}
}

func TestCLIBurstLossExitOne(t *testing.T) {
	out := filepath.Join(t.TempDir(), "bad")
	args := []string{
		"--fixture", "burst-loss", "--tracks", "2",
		"--bandwidth-kbps", "800,420", "--window", "2", "--out", out,
	}
	if code := run(args); code != exitBlocked {
		t.Fatalf("burst-loss exit = %d, want 1", code)
	}
}

func TestCLIResumeExitZeroAndSameHashes(t *testing.T) {
	badOut := filepath.Join(t.TempDir(), "bad")
	if code := run([]string{
		"--fixture", "burst-loss", "--tracks", "2",
		"--bandwidth-kbps", "800,420", "--window", "2", "--out", badOut,
	}); code != exitBlocked {
		t.Fatalf("baseline exit = %d, want 1", code)
	}
	recOut := filepath.Join(t.TempDir(), "rec")
	if code := run([]string{
		"--fixture", "repaired", "--tracks", "2",
		"--bandwidth-kbps", "800,420", "--window", "4",
		"--resume", badOut, "--out", recOut,
	}); code != exitOK {
		t.Fatalf("resume exit = %d, want 0", code)
	}
	validOut := filepath.Join(t.TempDir(), "valid")
	if code := run(baseArgs(validOut)); code != exitOK {
		t.Fatalf("full rerun exit = %d", code)
	}
	hashes := func(dir string) []string {
		data, err := os.ReadFile(filepath.Join(dir, "frames.json"))
		if err != nil {
			t.Fatal(err)
		}
		var m struct {
			Frames []struct {
				Hash string `json:"pixel_hash"`
			} `json:"frames"`
		}
		if err := json.Unmarshal(data, &m); err != nil {
			t.Fatal(err)
		}
		out := make([]string, len(m.Frames))
		for i, f := range m.Frames {
			out[i] = f.Hash
		}
		return out
	}
	a, b := hashes(recOut), hashes(validOut)
	if len(a) != len(b) {
		t.Fatalf("hash count differs: %d vs %d", len(a), len(b))
	}
	for i := range a {
		if a[i] != b[i] {
			t.Fatal("resume hashes differ from full rerun")
		}
	}
}

func TestCLIArgumentErrorsExitTwo(t *testing.T) {
	cases := [][]string{
		{"--tracks", "2", "--bandwidth-kbps", "1,2", "--window", "4",
			"--out", filepath.Join(t.TempDir(), "x")}, // missing --fixture
		{"--fixture", "nope", "--tracks", "1", "--bandwidth-kbps", "100",
			"--window", "4", "--out", filepath.Join(t.TempDir(), "x")},
		{"--fixture", "valid", "--tracks", "2", "--bandwidth-kbps", "100",
			"--window", "4", "--out", filepath.Join(t.TempDir(), "x")}, // count mismatch
		{"--fixture", "valid", "--tracks", "1", "--bandwidth-kbps", "100",
			"--window", "0", "--out", filepath.Join(t.TempDir(), "x")},
		{"--fixture", "valid", "--tracks", "1", "--bandwidth-kbps", "100",
			"--window", "4", "--format", "avi", "--out", filepath.Join(t.TempDir(), "x")},
	}
	for i, args := range cases {
		if code := run(args); code != exitBadArgs {
			t.Fatalf("case %d exit = %d, want 2", i, code)
		}
	}
}

func TestCLIDefaultBandwidthWhenOmitted(t *testing.T) {
	out := filepath.Join(t.TempDir(), "frames")
	code := run([]string{
		"--fixture", "valid", "--tracks", "3",
		"--window", "4", "--out", out,
	})
	if code != exitOK {
		t.Fatalf("omitted bandwidth must fall back to defaults, exit = %d", code)
	}
}

func TestCLITruncatedIsDegradedNotBlocked(t *testing.T) {
	out := filepath.Join(t.TempDir(), "trunc")
	code := run([]string{
		"--fixture", "truncated", "--tracks", "1",
		"--bandwidth-kbps", "800", "--window", "4", "--out", out,
	})
	if code != exitOK {
		t.Fatalf("truncation is accounted but non-blocking, exit = %d", code)
	}
}
