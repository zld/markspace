package main

import (
	"encoding/json"
	"io"
	"os"
	"strings"
	"testing"
)

// captureStdout runs fn while redirecting os.Stdout, returning its output.
func captureStdout(t *testing.T, fn func()) string {
	t.Helper()
	orig := os.Stdout
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	os.Stdout = w
	fn()
	_ = w.Close()
	os.Stdout = orig
	data, err := io.ReadAll(r)
	if err != nil {
		t.Fatal(err)
	}
	return string(data)
}

func TestSimulateAcceptanceExitCode(t *testing.T) {
	code := run([]string{
		"simulate", "--frames", "1200", "--seed", "42",
		"--loss-burst", "7", "--reorder", "12",
		"--duplicate", "0.03", "--json",
	})
	if code != exitOK {
		t.Fatalf("exit code = %d, want %d", code, exitOK)
	}
}

func TestParameterErrorsReturnCodeTwo(t *testing.T) {
	bad := [][]string{
		{"simulate", "--frames", "0"},
		{"simulate", "--frames", "-5"},
		{"simulate", "--window", "0"},
		{"simulate", "--loss-burst", "-1"},
		{"simulate", "--duplicate", "1.5"},
		{"simulate", "--corrupt", "-0.2"},
		{"simulate", "--loss-rate", "0.9", "--duplicate", "0.9"},
		{"bogus"},
		{},
		{"receive", "--read-timeout-ms", "0"},
	}
	for i, args := range bad {
		if got := run(args); got != exitParamError {
			t.Errorf("case %d %v: exit %d, want %d", i, args, got, exitParamError)
		}
	}
}

func TestReportSubcommandText(t *testing.T) {
	if code := run([]string{
		"report", "--frames", "300", "--seed", "3",
	}); code != exitOK {
		t.Fatalf("report exit = %d", code)
	}
}

func TestJSONShapeFromRealCommand(t *testing.T) {
	out := captureStdout(t, func() {
		code := run([]string{
			"simulate", "--frames", "600", "--seed", "9", "--json",
		})
		if code != exitOK {
			t.Fatalf("simulate exit = %d", code)
		}
	})
	var raw map[string]any
	if err := json.Unmarshal([]byte(out), &raw); err != nil {
		t.Fatalf("invalid JSON: %v", err)
	}
	for _, key := range []string{
		"frames", "received", "duplicate_drops", "late_drops",
		"invalid_packets", "concealed_frames", "max_conceal_run",
		"recovery_ratio", "buffer_level_curve",
	} {
		if _, ok := raw[key]; !ok {
			t.Errorf("missing JSON field %q", key)
		}
	}
	curve, ok := raw["buffer_level_curve"].([]any)
	if !ok || len(curve) == 0 {
		t.Fatal("buffer_level_curve missing or empty")
	}
}

func TestTextSummaryFromRealCommand(t *testing.T) {
	out := captureStdout(t, func() {
		if code := run([]string{
			"simulate", "--frames", "400", "--seed", "5",
		}); code != exitOK {
			t.Fatalf("simulate exit = %d", code)
		}
	})
	for _, token := range []string{
		"frames=400", "duplicate_drops=", "late_drops=",
		"invalid_packets=", "concealed_frames=", "buffer_level_curve_points=",
	} {
		if !strings.Contains(out, token) {
			t.Errorf("text output missing %q", token)
		}
	}
}
