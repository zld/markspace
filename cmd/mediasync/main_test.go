package main

import (
	"testing"

	"mediasync/internal/fixture"
)

func TestParseArgsValid(t *testing.T) {
	opts, err := parseArgs([]string{"demo", "--seed", "41", "--duration", "30s",
		"--bitrates", "1200000,2400000", "--out", "/tmp/x", "--case", "wrap"})
	if err != nil {
		t.Fatal(err)
	}
	if opts.seed != 41 || opts.duration != "30s" || opts.outDir != "/tmp/x" {
		t.Fatalf("parsed options wrong: %+v", opts)
	}
	if len(opts.bitrates) != 2 || opts.bitrates[0] != 1_200_000 {
		t.Fatalf("bitrates wrong: %v", opts.bitrates)
	}
	if opts.scenario != fixture.CaseWrap {
		t.Fatalf("scenario=%s", opts.scenario)
	}
}

func TestParseArgsDefaultsBitrates(t *testing.T) {
	opts, err := parseArgs([]string{"demo", "--duration", "5s"})
	if err != nil {
		t.Fatal(err)
	}
	if len(opts.bitrates) == 0 {
		t.Fatal("default bitrate ladder not applied")
	}
}

func TestParseArgsErrors(t *testing.T) {
	bad := [][]string{
		{"nope"},
		{"demo", "--seed"},
		{"demo", "--seed", "abc", "--bitrates", "1000"},
		{"demo", "--bitrates", "0", "--duration", "1s"},
		{"demo", "--bitrates", "1000", "--case", "bogus"},
		{"demo", "--unknown", "1"},
	}
	for _, args := range bad {
		if _, err := parseArgs(args); err == nil {
			t.Fatalf("expected error for %v", args)
		}
	}
}

func TestParseBitrates(t *testing.T) {
	got, err := parseBitrates("100, 200 ,300")
	if err != nil || len(got) != 3 || got[1] != 200 {
		t.Fatalf("parseBitrates=%v err=%v", got, err)
	}
	if _, err := parseBitrates("100,,200"); err == nil {
		t.Fatal("empty bitrate element accepted")
	}
}

func TestRunExitCodes(t *testing.T) {
	// Bad argument returns the documented code 2.
	if code := run([]string{"badcmd"}); code != exitBadArgs {
		t.Fatalf("bad args exit=%d want %d", code, exitBadArgs)
	}
	// A syntactically accepted but semantically invalid duration also maps to 2.
	if code := run([]string{"demo", "--duration", "5", "--bitrates", "1000"}); code != exitBadArgs {
		t.Fatalf("bad duration exit=%d want %d", code, exitBadArgs)
	}
	// A clean end-to-end run returns 0.
	dir := t.TempDir()
	code := run([]string{"demo", "--seed", "41", "--duration", "2s",
		"--bitrates", "1000000,2000000", "--out", dir})
	if code != exitOK {
		t.Fatalf("clean run exit=%d want %d", code, exitOK)
	}
}

func TestRunCorruptSurfacesDamage(t *testing.T) {
	dir := t.TempDir()
	code := run([]string{"demo", "--seed", "41", "--case", "corrupt",
		"--duration", "3s", "--out", dir})
	if code != exitOK {
		t.Fatalf("isolatable corruption should exit 0, got %d", code)
	}
}
