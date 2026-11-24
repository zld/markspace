package main

import "testing"

func TestParseFlags(t *testing.T) {
	o, err := parseFlags([]string{
		"--seed", "9", "--segment-packets", "12", "--queue-capacity", "1",
		"--recover", "--faults", "wrap,gap"})
	if err != nil {
		t.Fatal(err)
	}
	if o.seed != 9 || o.segmentPackets != 12 || o.queueCapacity != 1 ||
		!o.recover || len(o.faults) != 2 {
		t.Fatalf("parsed options wrong: %+v", o)
	}
}

func TestParseFlagsRejectsBadValues(t *testing.T) {
	for _, args := range [][]string{
		{"--queue-capacity", "0"},
		{"--faults", "nope"},
		{"--seed"},
		{"--bogus"},
	} {
		if _, err := parseFlags(args); err == nil {
			t.Fatalf("expected error for %v", args)
		}
	}
}

func TestRunRequiresDemoSubcommand(t *testing.T) {
	if code := run(nil); code != 1 {
		t.Fatalf("missing subcommand exit=%d want 1", code)
	}
}

func TestRunCleanDemoExitZero(t *testing.T) {
	code := run([]string{"demo", "--seed", "7",
		"--segment-packets", "20", "--queue-capacity", "2"})
	if code != 0 {
		t.Fatalf("clean demo exit=%d want 0", code)
	}
}

func TestRunRecoverableFaultsExitZero(t *testing.T) {
	code := run([]string{"demo", "--seed", "7",
		"--faults", "wrap,duplicate,gap,psi-version,audio-layout,truncated-tail",
		"--queue-capacity", "1", "--recover"})
	if code != 0 {
		t.Fatalf("recoverable faults exit=%d want 0", code)
	}
}

func TestRunFaultsWithoutRecoveryExitTwo(t *testing.T) {
	code := run([]string{"demo", "--seed", "7",
		"--faults", "audio-layout", "--queue-capacity", "1"})
	if code != 2 {
		t.Fatalf("unrecovered input fault exit=%d want 2", code)
	}
}
