package cli

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"tsrebuild/internal/report"
)

func writeSample(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	path := filepath.Join(dir, "sample.ts")
	if err := Generate(path, "remap-loss"); err != nil {
		t.Fatal(err)
	}
	return path
}

func TestGenerateWritesSyncPackets(t *testing.T) {
	path := filepath.Join(t.TempDir(), "out.ts")
	if err := Generate(path, "remap-loss"); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if len(data)%188 != 0 || data[0] != 0x47 {
		t.Fatalf("generated file is not synchronized 188-byte TS: %d bytes", len(data))
	}
}

func TestGenerateRejectsBadScenarioAndMissingOut(t *testing.T) {
	if err := Generate("/tmp/ignored.ts", "nope"); !AsUsageError(err) {
		t.Fatalf("expected usage error for bad scenario, got %v", err)
	}
	if err := Generate("", "basic"); !AsUsageError(err) {
		t.Fatalf("expected usage error for missing --out, got %v", err)
	}
}

func TestInspectJSON(t *testing.T) {
	src := writeSample(t)
	var out bytes.Buffer
	if err := Inspect(src, 1, true, &out); err != nil {
		t.Fatal(err)
	}
	var doc report.StreamReport
	if err := json.Unmarshal(out.Bytes(), &doc); err != nil {
		t.Fatalf("invalid JSON: %v", err)
	}
	if doc.PATVersionChanges != 1 || doc.PMTVersionChanges != 1 {
		t.Fatalf("version changes wrong: %+v", doc)
	}
	if len(doc.MappingIntervals) != 2 {
		t.Fatalf("expected 2 intervals, got %d", len(doc.MappingIntervals))
	}
}

func TestInspectRejectsBadProgram(t *testing.T) {
	src := writeSample(t)
	if err := Inspect(src, 0, true, &bytes.Buffer{}); !AsUsageError(err) {
		t.Fatalf("expected usage error, got %v", err)
	}
}

func TestInspectMissingFileIsRuntimeError(t *testing.T) {
	err := Inspect(filepath.Join(t.TempDir(), "absent.ts"), 1, true, &bytes.Buffer{})
	if err == nil || AsUsageError(err) {
		t.Fatalf("missing file must be a runtime (exit-1) error, got %v", err)
	}
}

func TestInspectStdin(t *testing.T) {
	src := writeSample(t)
	data, _ := os.ReadFile(src)

	r, w, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	old := os.Stdin
	os.Stdin = r
	defer func() { os.Stdin = old }()
	go func() { _, _ = w.Write(data); _ = w.Close() }()

	var out bytes.Buffer
	if err := Inspect("-", 1, true, &out); err != nil {
		t.Fatalf("stdin inspect failed: %v", err)
	}
	if !strings.Contains(out.String(), "pat_version_changes") {
		t.Fatalf("stdin report missing fields: %s", out.String())
	}
}

func TestSegmentEndToEnd(t *testing.T) {
	src := writeSample(t)
	outTS := filepath.Join(t.TempDir(), "program.ts")
	var rep bytes.Buffer
	if err := Segment(src, 1, outTS, true, &rep); err != nil {
		t.Fatal(err)
	}
	ts, err := os.ReadFile(outTS)
	if err != nil {
		t.Fatal(err)
	}
	if len(ts)%188 != 0 || ts[0] != 0x47 {
		t.Fatal("segment output is not synchronized TS")
	}
	var doc report.SegmentReport
	if err := json.Unmarshal(rep.Bytes(), &doc); err != nil {
		t.Fatalf("invalid segment report: %v", err)
	}
	if int64(len(ts)/188) != doc.WrittenPackets {
		t.Fatalf("output packets %d != report %d", len(ts)/188, doc.WrittenPackets)
	}
	if len(doc.Intervals) != 2 {
		t.Fatalf("expected 2 segment intervals, got %d", len(doc.Intervals))
	}
}

func TestSegmentRejectsMissingOut(t *testing.T) {
	if err := Segment(writeSample(t), 1, "", true, &bytes.Buffer{}); !AsUsageError(err) {
		t.Fatalf("expected usage error, got %v", err)
	}
}
