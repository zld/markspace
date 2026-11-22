package main

import (
	"io"
	"os"
	"strings"
	"testing"
)

// capture runs the CLI in-process and returns stdout, stderr and the exit code.
func capture(t *testing.T, args ...string) (string, string, int) {
	t.Helper()
	outR, outW, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	errR, errW, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	origOut, origErr := os.Stdout, os.Stderr
	os.Stdout, os.Stderr = outW, errW

	outCh := make(chan string, 1)
	errCh := make(chan string, 1)
	go func() {
		b, _ := io.ReadAll(outR)
		outCh <- string(b)
	}()
	go func() {
		b, _ := io.ReadAll(errR)
		errCh <- string(b)
	}()

	code := run(args)

	outW.Close()
	errW.Close()
	os.Stdout, os.Stderr = origOut, origErr
	return <-outCh, <-errCh, code
}

func TestDemoExitZeroAndReportedFields(t *testing.T) {
	if testing.Short() {
		t.Skip("demo build is a few hundred ms")
	}
	dir := t.TempDir()
	out, errOut, code := capture(t, "demo",
		"--seed", "7", "--duration", "12s", "--segment", "2s",
		"--bitrates", "400,900,1600", "--loss", "0.03",
		"--jitter", "80ms", "--late", "250ms",
		"--interrupt-after", "3", "--workdir", dir)
	if code != 0 {
		t.Fatalf("exit = %d\nstdout:\n%s\nstderr:\n%s", code, out, errOut)
	}
	for _, field := range []string{
		"av_sync_max_ms=", "packet_recovery_rate=", "p95_latency_ms=",
		"aligned_boundaries=", "resume_outputs=", "manifest_sha256=",
		"provenance_records=",
	} {
		if !strings.Contains(out, field) {
			t.Errorf("missing %q in output:\n%s", field, out)
		}
	}
	if !strings.Contains(out, "resume_outputs=true") {
		t.Errorf("resume_outputs not true:\n%s", out)
	}
}

func TestDemoIsRepeatableAcrossWorkingDirs(t *testing.T) {
	if testing.Short() {
		t.Skip("demo build is a few hundred ms")
	}
	args := func(dir string) []string {
		return []string{"demo", "--seed", "3", "--duration", "6s", "--segment", "2s",
			"--bitrates", "400,900", "--loss", "0.02", "--jitter", "20ms",
			"--late", "100ms", "--workdir", dir}
	}
	outA, errA, codeA := capture(t, args(t.TempDir())...)
	outB, errB, codeB := capture(t, args(t.TempDir())...)
	if codeA != 0 || codeB != 0 {
		t.Fatalf("exit codes %d/%d\n%s\n%s", codeA, codeB, errA, errB)
	}
	if outA != outB {
		t.Fatalf("output differs between runs:\n%s\n---\n%s", outA, outB)
	}
}

func TestVerifyExitZeroForAllCases(t *testing.T) {
	if testing.Short() {
		t.Skip("verify builds fixtures")
	}
	out, errOut, code := capture(t, "verify", "--seed", "7",
		"--case", "truncated,zero-timescale,late-event,sequence-wrap,burst-loss")
	if code != 0 {
		t.Fatalf("exit = %d\n%s\n%s", code, out, errOut)
	}
	for _, want := range []string{
		"error_count[truncated_container]=",
		"error_count[zero_timescale]=",
		"error_count[late_event]=",
		"error_count[unrecoverable_gap]=",
		"replay_interval=",
		"fault_count[sequence-wrap]=",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("missing %q:\n%s", want, out)
		}
	}
}

func TestVerifyRejectsUnknownCase(t *testing.T) {
	_, errOut, code := capture(t, "verify", "--case", "not-a-fault")
	if code != 1 {
		t.Fatalf("exit = %d", code)
	}
	if !strings.Contains(errOut, "unknown fault") {
		t.Fatalf("stderr = %q", errOut)
	}
}

func TestUnknownCommandAndHelp(t *testing.T) {
	if _, _, code := capture(t, "bogus"); code != 1 {
		t.Fatalf("unknown command exit = %d", code)
	}
	out, _, code := capture(t, "--help")
	if code != 0 {
		t.Fatalf("help exit = %d", code)
	}
	if !strings.Contains(out, "mediapipeline demo") {
		t.Fatalf("help output = %q", out)
	}
}

func TestParseDurationForms(t *testing.T) {
	cases := map[string]int64{
		"12s":   12_000_000_000,
		"250ms": 250_000_000,
		"250":   250_000_000, // bare integer means milliseconds
		"1m30s": 90_000_000_000,
	}
	for in, want := range cases {
		got, err := parseDuration(in)
		if err != nil {
			t.Errorf("parseDuration(%q): %v", in, err)
			continue
		}
		if int64(got) != want {
			t.Errorf("parseDuration(%q) = %d, want %d", in, got, want)
		}
	}
	for _, bad := range []string{"0", "-1s", "abc", ""} {
		if _, err := parseDuration(bad); err == nil {
			t.Errorf("parseDuration(%q) accepted", bad)
		}
	}
}

func TestParseNonNegativeDuration(t *testing.T) {
	for _, ok := range []string{"0", "0ms", "80ms"} {
		if _, err := parseNonNegativeDuration(ok); err != nil {
			t.Errorf("parseNonNegativeDuration(%q): %v", ok, err)
		}
	}
	if _, err := parseNonNegativeDuration("-5ms"); err == nil {
		t.Error("negative duration accepted")
	}
}

func TestParseBitrates(t *testing.T) {
	got, err := parseBitrates("400, 900 ,1600")
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 3 || got[0] != 400 || got[1] != 900 || got[2] != 1600 {
		t.Fatalf("bitrates = %v", got)
	}
	for _, bad := range []string{"", "0", "abc", "400,-1"} {
		if _, err := parseBitrates(bad); err == nil {
			t.Errorf("parseBitrates(%q) accepted", bad)
		}
	}
}

func TestDemoRejectsBadFlags(t *testing.T) {
	for _, args := range [][]string{
		{"demo", "--duration", "0s"},
		{"demo", "--segment", "-1s"},
		{"demo", "--bitrates", "x"},
		{"demo", "--loss", "2"},
	} {
		_, _, code := capture(t, append(args, "--workdir", t.TempDir())...)
		if code == 0 {
			t.Errorf("%v accepted", args)
		}
	}
}
