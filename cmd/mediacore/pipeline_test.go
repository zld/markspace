package main

import "testing"

func TestDemoPipelineMeetsAcceptance(t *testing.T) {
	o, err := parseArgs([]string{"demo", "--seed", "42", "--drift-ppm", "180",
		"--wrap-at", "4294967000", "--late-ms", "250", "--vfr"})
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	m, err := run(o)
	if err != nil {
		t.Fatalf("run: %v", err)
	}
	if m.RoundtripMatch != 1.0 || m.KeyframeHitRate != 1.0 || m.SegmentHashMatch != 1.0 {
		t.Fatalf("lossless invariants not all 1.0: %+v", m)
	}
	if m.RecoveredLoss < 1 {
		t.Fatalf("expected at least one recovered gap, got %d", m.RecoveredLoss)
	}
	if m.LateReplay < 1 {
		t.Fatalf("expected at least one late replay, got %d", m.LateReplay)
	}
	if abs(m.DriftPPM-180) > 5 {
		t.Fatalf("drift ppm error > 5: got %d", m.DriftPPM)
	}
	if m.Errors < 3 {
		t.Fatalf("expected at least 3 corrupt/boundary errors, got %d", m.Errors)
	}
	if !m.pass() {
		t.Fatalf("metrics did not pass acceptance gate: %s", m.String())
	}
}

func TestRepeatedRunsAreIdentical(t *testing.T) {
	o, _ := parseArgs([]string{"demo", "--seed", "42", "--drift-ppm", "180",
		"--wrap-at", "4294967000", "--late-ms", "250", "--vfr"})
	m1, _ := run(o)
	m2, _ := run(o)
	if m1.String() != m2.String() {
		t.Fatalf("non-deterministic runs:\n%s\n%s", m1.String(), m2.String())
	}
}

func TestArgumentValidationExitCodes(t *testing.T) {
	bad := [][]string{
		{"bogus"},
		{"demo", "--wrap-at", "9999999999"},
		{"--seed", "42"}, // missing demo subcommand
	}
	for _, args := range bad {
		if _, err := parseArgs(args); err == nil {
			t.Fatalf("expected validation error for %v", args)
		}
	}
	good := []string{"demo", "--seed", "1", "--vfr"}
	if _, err := parseArgs(good); err != nil {
		t.Fatalf("valid args rejected: %v", err)
	}
}

func abs(v int64) int64 {
	if v < 0 {
		return -v
	}
	return v
}
