package main

import (
	"os"
	"strings"
	"testing"

	"mediapipeline/internal/report"
)

// relTempDir returns a project-relative working directory (the CLI forbids
// absolute work-dir paths) and schedules its removal.
func relTempDir(t *testing.T) string {
	t.Helper()
	dir := ".test-tmp-" + strings.ReplaceAll(t.Name(), "/", "_")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(dir) })
	return dir
}

func validReport() report.Report {
	return report.Report{
		CaseCount: 3, FaultCount: 5, EarliestBadPacket: 1000,
		LocateErrorNS: 53333, RecoveryMS: 50,
		RollbackStage: "audio-normalize", CommitBoundary: "sync-align",
		SHA256:         strings.Repeat("a", 64),
		RepeatSHAEqual: true,
	}
}

func TestRunSuccessExitZero(t *testing.T) {
	code := run([]string{
		"--self-test", "--seed", "42", "--jitter-ms", "80", "--loss", "3",
		"--interrupt-after", "audio-normalize", "--work-dir", relTempDir(t),
	})
	if code != 0 {
		t.Fatalf("exit code = %d, want 0", code)
	}
}

func TestRunConfigErrorsExitTwo(t *testing.T) {
	cases := [][]string{
		{"--self-test", "--seed", "42", "--loss", "150", "--work-dir", relTempDir(t)},
		{"--self-test", "--seed", "42", "--interrupt-after", "nope", "--work-dir", relTempDir(t)},
		{"--self-test", "--seed", "42", "--work-dir", "/absolute/path"},
		{"--seed", "42"}, // no self-test flag
	}
	for _, args := range cases {
		if code := run(args); code != 2 {
			t.Errorf("args %v => exit %d, want 2", args, code)
		}
	}
}

func TestVerifyInvariants(t *testing.T) {
	if err := verifyInvariants(validReport()); err != nil {
		t.Fatalf("valid report rejected: %v", err)
	}
	good := validReport()
	badCases := []report.Report{
		func() report.Report { r := good; r.CaseCount = 2; return r }(),
		func() report.Report { r := good; r.FaultCount = 4; return r }(),
		func() report.Report { r := good; r.EarliestBadPacket = 500; return r }(),
		func() report.Report { r := good; r.LocateErrorNS = 2_000_000; return r }(),
		func() report.Report { r := good; r.RecoveryMS = 600; return r }(),
		func() report.Report { r := good; r.RollbackStage = ""; return r }(),
		func() report.Report { r := good; r.SHA256 = "short"; return r }(),
		func() report.Report { r := good; r.RepeatSHAEqual = false; return r }(),
	}
	for i, r := range badCases {
		if err := verifyInvariants(r); err == nil {
			t.Errorf("bad case %d should fail verification", i)
		}
	}
}
