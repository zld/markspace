package main

import (
	"encoding/json"
	"path/filepath"
	"testing"

	"github.com/example/mediad/internal/report"
)

func TestRunDemoProducesSummary(t *testing.T) {
	dir := t.TempDir()
	cfg := demoConfig{
		seed: 42, budgetMs: 5000, maxBytes: 1 << 20,
		targetRate: 48000, layout: "stereo", lufs: -16, truePeak: -1.0,
		workDir: filepath.Join(dir, "work"),
	}
	code := runDemo(cfg)
	// Demo includes a truncated sample by design, so input corruption (3) is
	// the expected exit code even when every recoverable item passes.
	if code != exitCorruptInput {
		t.Fatalf("exit code = %d, want %d", code, exitCorruptInput)
	}
}

func TestParseDemoRejectsBadLayout(t *testing.T) {
	if validLayout("quad") {
		t.Fatal("quad must not be a valid layout")
	}
	if !validLayout("mono") || !validLayout("stereo") {
		t.Fatal("mono and stereo must be valid")
	}
}

func TestSummaryShape(t *testing.T) {
	s := &report.Summary{
		Seed: 1,
		Items: []report.Item{
			{MaterialID: "x", Status: report.StatusSatisfied, OutputBytes: 10},
		},
	}
	raw, err := s.JSON()
	if err != nil {
		t.Fatal(err)
	}
	var decoded map[string]interface{}
	if err := json.Unmarshal(raw, &decoded); err != nil {
		t.Fatal(err)
	}
	if decoded["manifestSha256"] == nil {
		t.Fatal("manifest digest missing")
	}
}
