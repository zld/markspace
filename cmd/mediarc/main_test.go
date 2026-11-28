package main

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestRunDemoSuccess(t *testing.T) {
	dir := t.TempDir()
	var stdout, stderr bytes.Buffer
	code := run([]string{"demo", "--seed", "7", "--window", "4", "--max-buffer", "8", "--out", dir}, &stdout, &stderr)
	if code != exitOK {
		t.Fatalf("exit = %d, stderr = %s", code, stderr.String())
	}
	out := stdout.String()
	for _, want := range []string{"frame hash", "segment hash", "pixel mean", "loudness peak",
		"packets lost", "packets recovered", "first bad offset", "max align error", "RESULT PASS"} {
		if !strings.Contains(out, want) {
			t.Fatalf("output is missing %q:\n%s", want, out)
		}
	}
	for _, name := range []string{"subtitles.srt", "truncated.mp4", "lossy.rtp", "manifest.txt"} {
		path := filepath.Join(dir, "fixtures", name)
		if name == "manifest.txt" {
			path = filepath.Join(dir, name)
		}
		if _, err := os.Stat(path); err != nil {
			t.Fatalf("expected %s: %v", path, err)
		}
	}
	if stdout.Len() == 0 {
		t.Fatal("no report on stdout")
	}
}

func TestRunDemoDeterministic(t *testing.T) {
	var first, second bytes.Buffer
	if code := run([]string{"demo", "--out", t.TempDir()}, &first, &bytes.Buffer{}); code != exitOK {
		t.Fatalf("first run exit %d", code)
	}
	if code := run([]string{"demo", "--out", t.TempDir()}, &second, &bytes.Buffer{}); code != exitOK {
		t.Fatalf("second run exit %d", code)
	}
	strip := func(s string) string {
		var kept []string
		for _, line := range strings.Split(s, "\n") {
			if strings.Contains(line, "elapsed") || strings.Contains(line, "output=") ||
				strings.Contains(line, "manifest") {
				continue
			}
			kept = append(kept, line)
		}
		return strings.Join(kept, "\n")
	}
	if strip(first.String()) != strip(second.String()) {
		t.Fatalf("reports differ:\n%s\n---\n%s", first.String(), second.String())
	}
}

func TestRunBadParams(t *testing.T) {
	cases := [][]string{
		{},
		{"nonsense"},
		{"demo", "--window", "0"},
		{"demo", "--window", "-1"},
		{"demo", "--max-buffer", "0"},
		{"demo", "--no-such-flag"},
		{"demo", "extra"},
	}
	for _, args := range cases {
		var stdout, stderr bytes.Buffer
		if code := run(args, &stdout, &stderr); code != exitBadParams {
			t.Fatalf("args %v: exit = %d, want %d", args, code, exitBadParams)
		}
	}
}

func TestRunHelp(t *testing.T) {
	var stdout, stderr bytes.Buffer
	if code := run([]string{"help"}, &stdout, &stderr); code != exitOK {
		t.Fatalf("help exit = %d", code)
	}
	if !strings.Contains(stdout.String(), "mediarc demo") {
		t.Fatalf("help output = %q", stdout.String())
	}
}

func TestReportContainsAllFixtures(t *testing.T) {
	dir := t.TempDir()
	var stdout bytes.Buffer
	if code := run([]string{"demo", "--out", dir}, &stdout, &bytes.Buffer{}); code != exitOK {
		t.Fatalf("exit = %d", code)
	}
	entries, err := os.ReadDir(filepath.Join(dir, "fixtures"))
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) == 0 {
		t.Fatal("no fixtures generated")
	}
	manifest, err := os.ReadFile(filepath.Join(dir, "manifest.txt"))
	if err != nil {
		t.Fatal(err)
	}
	text := string(manifest)
	for _, want := range []string{"frame-hash=", "segment-hash=", "pixel-mean=", "peak-dbfs=",
		"packets-lost=", "recovered=", "first-unreadable=", "max-alignment-error-ns=", "fixtures:"} {
		if !strings.Contains(text, want) {
			t.Fatalf("manifest is missing %q:\n%s", want, text)
		}
	}
	if strings.Contains(text, "failures:") {
		t.Fatalf("manifest reports failures:\n%s", text)
	}
}
