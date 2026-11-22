package main

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestDemoExitsZeroAndPrintsAcceptanceLines(t *testing.T) {
	dir := t.TempDir()
	var out, errBuf bytes.Buffer
	code := run([]string{"demo", "--seed", "41", "--loss-burst", "3",
		"--bandwidth-kbps", "1800", "--segment-ms", "2000", "--out", dir},
		&out, &errBuf)
	if code != exitOK {
		t.Fatalf("exit %d, stderr=%s", code, errBuf.String())
	}
	text := out.String()
	wants := []string{
		"roundtrip_match=98/100",
		"keyframe_hit=9/10",
		"duplicate_packets=2",
		"reordered_packets=4",
		"recovered_gaps=1",
		"damaged_spans=1",
		"repeat_run_match=true",
		"thresholds_met=true",
		"segment_sha256=",
	}
	for _, want := range wants {
		if !strings.Contains(text, want) {
			t.Fatalf("output missing %q\n%s", want, text)
		}
	}
	// The segment hash is 64 lowercase hex characters.
	for _, line := range strings.Split(text, "\n") {
		if !strings.HasPrefix(line, "segment_sha256=") {
			continue
		}
		hash := strings.TrimPrefix(line, "segment_sha256=")
		if len(hash) != 64 {
			t.Fatalf("segment hash length %d: %q", len(hash), hash)
		}
		for _, c := range hash {
			if !strings.ContainsRune("0123456789abcdef", c) {
				t.Fatalf("segment hash is not hex: %q", hash)
			}
		}
	}
	// Outputs exist and the report is named.
	if _, err := os.Stat(filepath.Join(dir, "media.mp4")); err != nil {
		t.Fatalf("media.mp4 missing: %v", err)
	}
	if _, err := os.Stat(filepath.Join(dir, "report.json")); err != nil {
		t.Fatalf("report.json missing: %v", err)
	}
}

func TestDemoIsReproducible(t *testing.T) {
	dirA, dirB := t.TempDir(), t.TempDir()
	args := []string{"demo", "--seed", "7", "--loss-burst", "3",
		"--bandwidth-kbps", "1800", "--segment-ms", "2000", "--out"}
	var a, b, errBuf bytes.Buffer
	if code := run(append(args, dirA), &a, &errBuf); code != exitOK {
		t.Fatalf("first run exit %d: %s", code, errBuf.String())
	}
	if code := run(append(args, dirB), &b, &errBuf); code != exitOK {
		t.Fatalf("second run exit %d: %s", code, errBuf.String())
	}
	hashOf := func(text string) string {
		for _, line := range strings.Split(text, "\n") {
			if strings.HasPrefix(line, "segment_sha256=") {
				return line
			}
		}
		return ""
	}
	if hashOf(a.String()) == "" || hashOf(a.String()) != hashOf(b.String()) {
		t.Fatalf("segment hashes differ:\n%s\n%s", hashOf(a.String()), hashOf(b.String()))
	}
	for _, name := range []string{"media.mp4", "report.json"} {
		ab, err := os.ReadFile(filepath.Join(dirA, name))
		if err != nil {
			t.Fatal(err)
		}
		bb, err := os.ReadFile(filepath.Join(dirB, name))
		if err != nil {
			t.Fatal(err)
		}
		// report.json records absolute paths, so compare media bytes only.
		if name == "media.mp4" && !bytes.Equal(ab, bb) {
			t.Fatalf("%s differs between runs", name)
		}
	}
}

func TestDemoRejectsBadInput(t *testing.T) {
	cases := [][]string{
		{},
		{"unknown"},
		{"demo", "--bandwidth-kbps", "0"},
		{"demo", "--segment-ms", "-1"},
		{"demo", "--seed", "-1"},
		{"demo", "--unknown-tracks", "ignore"},
		{"demo", "extra-arg"},
		{"demo", "--loss-burst", "4"}, // >= packets per sample
	}
	for _, args := range cases {
		var out, errBuf bytes.Buffer
		if code := run(args, &out, &errBuf); code != exitInput {
			t.Fatalf("args %v: exit %d want %d", args, code, exitInput)
		}
	}
}

func TestDemoLossBeyondRecoveryFailsThreshold(t *testing.T) {
	// With a very large burst the fixture itself is rejected as an input
	// error, which is the correct boundary: a burst that erases whole samples
	// cannot be represented by this generator.
	var out, errBuf bytes.Buffer
	code := run([]string{"demo", "--loss-burst", "9"}, &out, &errBuf)
	if code != exitInput {
		t.Fatalf("exit %d want %d: %s", code, exitInput, errBuf.String())
	}
}
