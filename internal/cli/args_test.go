package cli

import (
	"bytes"
	"strings"
	"testing"

	"subtitle/internal/cue"
)

func TestParseVerifyRequiredFlags(t *testing.T) {
	if _, err := ParseVerify(nil); err == nil || !IsUsage(err) {
		t.Fatal("missing flags must be a usage error")
	}
	if _, err := ParseVerify([]string{"--input", "x.srt"}); err == nil {
		t.Fatal("--to is required")
	}
}

func TestParseVerifyHappyPath(t *testing.T) {
	va, err := ParseVerify([]string{
		"--input", "scene.srt", "--output", "scene.ass",
		"--to", "ass", "--roundtrip", "srt",
		"--fps", "24000/1001", "--overlap", "drop-later",
		"--encoding", "utf-8", "--jobs", "4",
	})
	if err != nil {
		t.Fatal(err)
	}
	if va.To != cue.FormatASS || va.Roundtrip != cue.FormatSRT || va.Strategy != "drop-later" {
		t.Fatalf("parsed formats wrong: %+v", va)
	}
	if va.Rate.Num != 24000 || va.Rate.Den != 1001 || va.Jobs != 4 || va.IsBatch {
		t.Fatalf("parsed values wrong: %+v", va)
	}
}

func TestParseVerifyBatchModeAndValidation(t *testing.T) {
	va, err := ParseVerify([]string{"--input", "subs", "--to", "vtt"})
	if err != nil {
		t.Fatal(err)
	}
	if !va.IsBatch {
		t.Fatal("missing --output implies batch mode")
	}
	for _, tc := range [][]string{
		{"--input", "x", "--to", "ass", "--fps", "bad"},
		{"--input", "x", "--to", "ass", "--overlap", "merge"},
		{"--input", "x", "--to", "xml"},
		{"--input", "x", "--to", "ass", "--jobs", "0"},
		{"--input", "x", "--to", "ass", "--encoding", "latin1"},
	} {
		if _, err := ParseVerify(tc); err == nil {
			t.Fatalf("expected usage error for %v", tc)
		}
	}
}

func TestRunExitCodes(t *testing.T) {
	// Subcommand dispatch lives in package main; here we exercise the help
	// text contract the entry point relies on.
	if !strings.Contains(HelpText, "verify") || !strings.Contains(HelpText, "fixture") {
		t.Fatal("help text must document both subcommands")
	}
	var buf bytes.Buffer
	if code := runHelp(&buf); code != ExitOK || !strings.Contains(buf.String(), "subtitle") {
		t.Fatalf("help code=%d", code)
	}
}
