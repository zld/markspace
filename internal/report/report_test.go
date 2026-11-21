package report

import (
	"bytes"
	"encoding/json"
	"strings"
	"testing"

	"streamcheck/internal/config"
	"streamcheck/internal/validate"
)

func TestBuildAndWriteJSON(t *testing.T) {
	cfg := &config.Build{Input: "/tmp/in", Pattern: "seg_{rate}_{index}.ts", SegmentSeconds: 4, ToleranceMS: 80}
	checks := []validate.RateCheck{
		{Rate: 1600, SegmentCount: 6, TotalMS: 24000, DurationDeltaMS: 12, ManifestPath: "/tmp/in/media_1600.m3u8"},
		{Rate: 800, SegmentCount: 5, TotalMS: 20000, MissingIDs: []int{5},
			DuplicateIDs: []int{2}, Issues: []string{"duplicate_segment_id 2: a, b"}},
	}
	r := Build(cfg, checks, nil, []string{"ignored 2 files"}, map[int]string{800: "abc", 1600: "def"})

	var buf bytes.Buffer
	if err := WriteJSON(&buf, r); err != nil {
		t.Fatal(err)
	}
	var doc map[string]any
	if err := json.Unmarshal(buf.Bytes(), &doc); err != nil {
		t.Fatalf("invalid JSON: %v\n%s", err, buf.String())
	}
	rates := doc["rates"].([]any)
	if len(rates) != 2 {
		t.Fatalf("rates = %v", rates)
	}
	first := rates[0].(map[string]any)
	if first["rate_kbps"].(float64) != 800 {
		t.Fatalf("rates not sorted: %v", rates)
	}
	for _, key := range []string{"rate_kbps", "segment_count", "total_duration_seconds",
		"duration_delta_ms", "missing_ranges", "duplicate_ids", "validation_failures",
		"manifest_sha256", "manifest_path"} {
		if _, ok := first[key]; !ok {
			t.Errorf("rate report missing %q", key)
		}
	}
	if !r.Failed || r.ExitCode != 1 {
		t.Fatalf("report = %+v", r)
	}
	if first["manifest_sha256"].(string) != "abc" {
		t.Fatalf("digest = %v", first["manifest_sha256"])
	}
}

func TestEmptyReportShape(t *testing.T) {
	cfg := &config.Build{Input: "/tmp/in", ToleranceMS: 80}
	r := Build(cfg, []validate.RateCheck{{Rate: 800, TotalMS: 8000}}, nil, nil, nil)
	var buf bytes.Buffer
	if err := WriteJSON(&buf, r); err != nil {
		t.Fatal(err)
	}
	out := buf.String()
	for _, want := range []string{`"missing_ranges": []`, `"duplicate_ids": []`, `"validation_failures": []`, `"exit_code": 0`} {
		if !strings.Contains(out, want) {
			t.Errorf("JSON missing %s:\n%s", want, out)
		}
	}
	if r.Failed {
		t.Fatal("clean report marked failed")
	}
}

func TestWriteText(t *testing.T) {
	cfg := &config.Build{Input: "/tmp/in", Pattern: "seg_{rate}_{index}.ts", ToleranceMS: 80}
	r := Build(cfg, []validate.RateCheck{{
		Rate: 800, SegmentCount: 6, TotalMS: 24000, DurationDeltaMS: 4,
		ManifestPath: "/tmp/in/media_800.m3u8", Issues: []string{"probe_failed x.ts: broken"},
	}}, []string{"changed_file x.ts: size 1, was 2"}, nil, map[int]string{800: "deadbeef"})
	var buf bytes.Buffer
	WriteText(&buf, r)
	out := buf.String()
	for _, want := range []string{"rate 800 kbps", "24.000s", "deadbeef", "probe_failed", "changed_file", "result: FAIL (exit 1)"} {
		if !strings.Contains(out, want) {
			t.Errorf("text report missing %q:\n%s", want, out)
		}
	}
}
