package cli

import (
	"io"
	"strings"
	"testing"

	"loudscan/internal/report"
)

func parse(t *testing.T, args ...string) (Config, error) {
	t.Helper()
	return ParseScan(args, io.Discard)
}

func TestParseScanDefaults(t *testing.T) {
	cfg, err := parse(t, "--input", "/tmp/a")
	if err != nil {
		t.Fatal(err)
	}
	if cfg.IntegratedMax != -16 || cfg.TruePeakMax != -1 {
		t.Errorf("limits = %v/%v", cfg.IntegratedMax, cfg.TruePeakMax)
	}
	if cfg.Format != report.FormatTable {
		t.Errorf("format = %v", cfg.Format)
	}
	if cfg.Workers < 1 || cfg.Workers > 8 {
		t.Errorf("workers = %d", cfg.Workers)
	}
	if len(cfg.Inputs) != 1 || cfg.Inputs[0] != "/tmp/a" {
		t.Errorf("inputs = %v", cfg.Inputs)
	}
}

func TestParseScanRepeatableInput(t *testing.T) {
	cfg, err := parse(t, "--input", "/a", "--input", "/b", "--input", "/c")
	if err != nil {
		t.Fatal(err)
	}
	if len(cfg.Inputs) != 3 {
		t.Fatalf("inputs = %v", cfg.Inputs)
	}
}

func TestParseScanRejectsBadValues(t *testing.T) {
	cases := [][]string{
		{},                                    // no input
		{"--input", "/a", "--workers", "0"},   // too few workers
		{"--input", "/a", "--workers", "9"},   // too many
		{"--input", "/a", "--format", "yaml"}, // unknown format
		{"--input", "/a", "--integrated-max", "-90"}, // below range
		{"--input", "/a", "--integrated-max", "3"},   // above range
		{"--input", "/a", "--true-peak-max", "20"},   // above range
		{"--input", "/a", "--max-seconds", "0"},      // invalid limit
		{"--input", "/a", "--timeout", "0s"},         // invalid timeout
		{"--input", "/a", "extra"},                   // stray argument
		{"--input", ""},                              // empty path
	}
	for _, args := range cases {
		if _, err := parse(t, args...); err == nil {
			t.Errorf("%v accepted", args)
		}
	}
}

func TestParseScanAllowsDisabledLimits(t *testing.T) {
	cfg, err := parse(t, "--input", "/a", "--max-bytes", "-1", "--max-seconds", "-1")
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Limits.MaxBytes != -1 || cfg.Limits.MaxSeconds != -1 {
		t.Errorf("limits = %+v", cfg.Limits)
	}
}

func TestParseScanJSON(t *testing.T) {
	cfg, err := parse(t, "--input", "/a", "--format", "json")
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Format != report.FormatJSON {
		t.Errorf("format = %v", cfg.Format)
	}
}

func TestParseFixture(t *testing.T) {
	cfg, err := ParseFixture([]string{"--dir", "/tmp/x", "--seed", "42"}, io.Discard)
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Dir != "/tmp/x" || cfg.Seed != 42 {
		t.Errorf("cfg = %+v", cfg)
	}
	if _, err := ParseFixture([]string{"--dir", " "}, io.Discard); err == nil {
		t.Error("blank dir accepted")
	}
	if _, err := ParseFixture([]string{"stray"}, io.Discard); err == nil {
		t.Error("stray argument accepted")
	}
}

func TestSortInputsDeterministic(t *testing.T) {
	got := SortInputs([]string{"c", "a", "b"})
	if strings.Join(got, ",") != "a,b,c" {
		t.Errorf("got %v", got)
	}
	// The caller's slice must not be reordered in place.
	in := []string{"c", "a"}
	_ = SortInputs(in)
	if in[0] != "c" {
		t.Errorf("input reordered: %v", in)
	}
}
