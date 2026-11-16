package main

import (
	"os"
	"path/filepath"
	"testing"
)

func TestCLIDemoBuildVerify(t *testing.T) {
	dir := t.TempDir()
	inputDir := filepath.Join(dir, "input")
	planPath := filepath.Join(inputDir, "plan.json")
	mpdPath := filepath.Join(dir, "stream.mpd")

	if code := run([]string{"demo", "--out", inputDir}); code != exitOK {
		t.Fatalf("demo exit = %d, want 0", code)
	}
	if _, err := os.Stat(planPath); err != nil {
		t.Fatalf("plan.json not written: %v", err)
	}

	code := run([]string{
		"build", "--input", planPath, "--out", mpdPath,
		"--template", "seg-$RepresentationID$-$Number%05d$.m4s",
		"--start-tolerance", "20ms", "--duration-tolerance", "2ms",
	})
	if code != exitOK {
		t.Fatalf("build exit = %d, want 0", code)
	}
	if _, err := os.Stat(mpdPath); err != nil {
		t.Fatalf("mpd not written: %v", err)
	}

	if code := run([]string{"verify", "--mpd", mpdPath, "--plan", planPath}); code != exitOK {
		t.Fatalf("verify exit = %d, want 0", code)
	}
}

func TestCLIExitCodes(t *testing.T) {
	dir := t.TempDir()
	inputDir := filepath.Join(dir, "input")
	planPath := filepath.Join(inputDir, "plan.json")
	mpdPath := filepath.Join(dir, "stream.mpd")
	run([]string{"demo", "--out", inputDir})

	// An ordinary file occupying a path that build wants as a directory.
	blocker := filepath.Join(dir, "blocker")
	if err := os.WriteFile(blocker, []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	blockedOut := filepath.Join(blocker, "x.mpd")

	cases := []struct {
		name string
		args []string
		want int
	}{
		{"no subcommand", nil, exitInput},
		{"unknown subcommand", []string{"frobnicate"}, exitInput},
		{"missing flags", []string{"build"}, exitInput},
		{"bad tolerance unit", []string{"build", "--input", planPath, "--out", mpdPath,
			"--template", "a-$RepresentationID$-$Number$.m4s", "--start-tolerance", "20"}, exitInput},
		{"unreadable input", []string{"build", "--input", filepath.Join(dir, "nope.json"),
			"--out", mpdPath, "--template", "a-$RepresentationID$-$Number$.m4s"}, exitFileError},
		{"unwritable output", []string{"build", "--input", planPath,
			"--out", blockedOut, "--template", "a-$RepresentationID$-$Number$.m4s"}, exitFileError},
		{"traversal template", []string{"build", "--input", planPath, "--out", mpdPath,
			"--template", "../$RepresentationID$.m4s"}, exitInput},
		{"duplicate path template", []string{"build", "--input", planPath, "--out", mpdPath,
			"--template", "fixed-$Number$.m4s"}, exitInput},
		{"tolerance breach", []string{"build", "--input", planPath,
			"--out", filepath.Join(dir, "breach.mpd"), "--template",
			"a-$RepresentationID$-$Number$.m4s", "--start-tolerance", "10ms"}, exitTimeline},
		{"verify missing mpd", []string{"verify", "--mpd", filepath.Join(dir, "nope.mpd"),
			"--plan", planPath}, exitFileError},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := run(tc.args); got != tc.want {
				t.Fatalf("run(%v) exit = %d, want %d", tc.args, got, tc.want)
			}
		})
	}
}

func TestCLITamperedVerifyFails(t *testing.T) {
	dir := t.TempDir()
	inputDir := filepath.Join(dir, "input")
	planPath := filepath.Join(inputDir, "plan.json")
	mpdPath := filepath.Join(dir, "stream.mpd")
	run([]string{"demo", "--out", inputDir})
	run([]string{"build", "--input", planPath, "--out", mpdPath,
		"--template", "seg-$RepresentationID$-$Number%05d$.m4s"})

	// Overwrite with unparseable XML; verify must reject it.
	if err := os.WriteFile(mpdPath, []byte(`<?xml version="1.0"?><MPD><unterminated>`), 0o644); err != nil {
		t.Fatal(err)
	}
	if code := run([]string{"verify", "--mpd", mpdPath, "--plan", planPath}); code != exitInput {
		t.Fatalf("tampered mpd exit = %d, want %d", code, exitInput)
	}
}
