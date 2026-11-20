package tests

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"jitterlab/internal/metrics"
	"jitterlab/internal/report"
)

func TestReportEmptyAndAtomic(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "report.json")
	rep := report.New(report.Run{Mode: "simulate"}, metrics.New())
	if err := rep.WriteJSON(path); err != nil {
		t.Fatalf("WriteJSON: %v", err)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	var doc map[string]json.RawMessage
	if err := json.Unmarshal(data, &doc); err != nil {
		t.Fatalf("decode: %v", err)
	}
	sum := struct {
		PacketsReceived int     `json:"packets_received"`
		BufferSamples   int     `json:"buffer_samples"`
		ContinuityRatio float64 `json:"continuity_ratio"`
		Samples         []any   `json:"samples"`
	}{}
	if err := json.Unmarshal(doc["summary"], &sum); err != nil {
		t.Fatalf("decode summary: %v", err)
	}
	if sum.PacketsReceived != 0 || sum.BufferSamples != 0 || sum.ContinuityRatio != 0 {
		t.Fatalf("empty report not zero-valued: %+v", sum)
	}
	if sum.Samples == nil {
		t.Fatal("samples must be [] not null")
	}
	// No temp files may be left behind.
	entries, _ := os.ReadDir(dir)
	if len(entries) != 1 {
		t.Fatalf("temp files left behind: %v", entries)
	}
	// A second write must replace atomically and stay valid.
	if err := rep.WriteJSON(path); err != nil {
		t.Fatalf("second WriteJSON: %v", err)
	}
	if _, err := os.Stat(path); err != nil {
		t.Fatalf("report missing after replace: %v", err)
	}
}
