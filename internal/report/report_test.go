package report

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"bitrack/internal/model"
)

func sampleCheckpoint() *model.Checkpoint {
	segs := []*model.Segment{
		{Index: 0, State: model.StateCommitted, TargetBits: 1000, ActualBits: 980, PSNRDB: 40, FrameCount: 10},
		{Index: 1, State: model.StateCommitted, TargetBits: 2000, ActualBits: 2050, PSNRDB: 35, FrameCount: 20},
	}
	return &model.Checkpoint{
		Version: 1, JobID: "j", InputSHA256: "sha", BaselineDigest: "bd",
		Params:   model.Params{TargetKbps: 300, MaxPSNRDropDB: 1},
		Probe:    model.ProbeInfo{NbFrames: 30, DurationSec: 1.25},
		Segments: segs, CreatedAt: time.Now(), UpdatedAt: time.Now(),
	}
}

func TestBuildSummaryFields(t *testing.T) {
	cp := sampleCheckpoint()
	sum := BuildSummary(cp, "run", 1)
	if sum.TargetKbps != 300 {
		t.Errorf("target = %d", sum.TargetKbps)
	}
	if len(sum.Segments) != 2 {
		t.Fatalf("segments = %d", len(sum.Segments))
	}
	// actual = (980+2050)*8 / 1.25 / 1000 = 19.392 kbps
	if sum.ActualKbps <= 0 {
		t.Error("actual kbps should be computed")
	}
	if sum.SegmentPSNRStdDevDB <= 0 {
		t.Error("stddev must be positive for differing PSNR values")
	}
	if sum.ReusedSegments != 1 {
		t.Errorf("reused = %d, want 1", sum.ReusedSegments)
	}
}

func TestBaselineSaveLoadAndBinding(t *testing.T) {
	dir := t.TempDir()
	cp := sampleCheckpoint()
	path, err := SaveBaseline(dir, cp)
	if err != nil {
		t.Fatal(err)
	}
	rec, err := LoadBaseline(path, "sha", "bd")
	if err != nil {
		t.Fatalf("load baseline: %v", err)
	}
	if rec.TargetKbps != 300 || len(rec.Segments) != 2 {
		t.Errorf("baseline content wrong: %+v", rec)
	}
	// Fingerprint mismatch must be rejected with a locatable reason.
	if _, err := LoadBaseline(path, "different", "bd"); err == nil ||
		!strings.Contains(err.Error(), "fingerprint mismatch") {
		t.Fatalf("fingerprint mismatch must reject, got %v", err)
	}
	// Parameter digest mismatch must be rejected.
	if _, err := LoadBaseline(path, "sha", "other-digest"); err == nil ||
		!strings.Contains(err.Error(), "parameter digest mismatch") {
		t.Fatalf("digest mismatch must reject, got %v", err)
	}
}

func TestCorruptBaselineRejected(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, BaselineFileName)
	if err := os.WriteFile(path, []byte("{broken"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := LoadBaseline(path, "x", "y"); err == nil ||
		!strings.Contains(err.Error(), "corrupt") {
		t.Fatalf("corrupt baseline must be rejected, got %v", err)
	}
	if err := CorruptBaselineForTesting(path); err != nil {
		t.Fatal(err)
	}
	if _, err := LoadBaseline(path, "x", "y"); err == nil {
		t.Fatal("deliberately corrupted baseline must fail to parse")
	}
}

func TestWriteJSONAtomic(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "out.json")
	if err := WriteJSON(path, map[string]int{"a": 1}); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(path + ".tmp"); !os.IsNotExist(err) {
		t.Error("temp file should not remain after atomic write")
	}
	data, err := os.ReadFile(path)
	if err != nil || !strings.Contains(string(data), `"a": 1`) {
		t.Errorf("written content wrong: %q", data)
	}
}
