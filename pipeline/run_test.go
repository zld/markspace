package pipeline

import (
	"math"
	"path/filepath"
	"testing"

	"mediapack/model"
	"mediapack/segment"
)

func testConfig(t *testing.T, name string) Config {
	t.Helper()
	return Config{
		Seed: 17, BitrateCount: 2, QueueCapacity: 8,
		OutputDir: filepath.Join(t.TempDir(), name), ReportFormat: "json",
	}
}

func TestRunNormalMetricsBoundariesAndResume(t *testing.T) {
	cfg := testConfig(t, "demo")
	res, err := Run(cfg)
	if err != nil {
		t.Fatal(err)
	}
	if !res.OK || res.Status != "ok" {
		t.Fatalf("normal run should be clean: status=%s warnings=%+v", res.Status, res.Warnings)
	}
	if res.Metrics.AVSyncErrorMS > 20 {
		t.Fatalf("AV sync error %.3fms exceeds 20ms", res.Metrics.AVSyncErrorMS)
	}
	if math.IsNaN(res.Metrics.P95LatencyMS) || math.IsInf(res.Metrics.P95LatencyMS, 0) || res.Metrics.P95LatencyMS < 0 {
		t.Fatalf("P95 must be finite non-negative: %f", res.Metrics.P95LatencyMS)
	}
	if res.Metrics.PeakQueueOccupancy > cfg.QueueCapacity {
		t.Fatalf("peak %d exceeds capacity %d", res.Metrics.PeakQueueOccupancy, cfg.QueueCapacity)
	}
	if res.Metrics.RepresentationCount != 2 || res.Metrics.SegmentCount != 8 {
		t.Fatalf("unexpected segment counts: %+v", res.Metrics)
	}
	if len(res.Metrics.SegmentCountPerRepresentation) != 2 ||
		res.Metrics.SegmentCountPerRepresentation[0] != res.Metrics.SegmentCountPerRepresentation[1] {
		t.Fatalf("representation segment counts differ: %v", res.Metrics.SegmentCountPerRepresentation)
	}

	manifest, digest, err := segment.ReadManifest(cfg.OutputDir)
	if err != nil {
		t.Fatal(err)
	}
	if digest != res.ManifestSHA256 {
		t.Fatal("result and manifest digest differ")
	}
	for i := 1; i < len(manifest.Representations); i++ {
		a, b := manifest.Representations[0].Segments, manifest.Representations[i].Segments
		if len(a) != len(b) {
			t.Fatal("representation segment counts differ")
		}
		for j := range a {
			if a[j].StartNanos != b[j].StartNanos || a[j].EndNanos != b[j].EndNanos {
				t.Fatalf("boundary mismatch at segment %d: %+v vs %+v", j, a[j], b[j])
			}
		}
	}

	cfg.Resume = true
	resume, err := Run(cfg)
	if err != nil {
		t.Fatal(err)
	}
	if resume.Resume == nil || resume.Resume.VerifiedSegments != res.Metrics.SegmentCount {
		t.Fatalf("expected all segments verified: %+v", resume.Resume)
	}
	if resume.Resume.ContinuedSegments != 0 || resume.ManifestSHA256 != res.ManifestSHA256 {
		t.Fatalf("resume changed continuation/digest: %+v", resume.Resume)
	}
}

func TestRunResumeRejectsTamperedSegmentAndManifestMismatch(t *testing.T) {
	cfg := testConfig(t, "tamper")
	if _, err := Run(cfg); err != nil {
		t.Fatal(err)
	}
	manifest, _, err := segment.ReadManifest(cfg.OutputDir)
	if err != nil {
		t.Fatal(err)
	}
	ref := manifest.Representations[0].Segments[0]
	if _, err := segment.WriteFileSafe(cfg.OutputDir, ref.Path, []byte("tampered")); err != nil {
		t.Fatal(err)
	}
	cfg.Resume = true
	if _, err := Run(cfg); !model.IsCategory(err, model.CategoryChecksumMismatch) {
		t.Fatalf("expected checksum mismatch, got %v", err)
	}
	if code := ExitCodeFor(model.NewError("", "", -1, model.CategoryChecksumMismatch, "", nil)); code != 3 {
		t.Fatalf("checksum mismatch exit code = %d", code)
	}

	other := testConfig(t, "mismatch")
	if _, err := Run(other); err != nil {
		t.Fatal(err)
	}
	other.Resume = true
	other.Seed = 18
	_, err = Run(other)
	if !model.IsCategory(err, model.CategoryUnsafeResume) {
		t.Fatalf("expected unsafe resume, got %v", err)
	}
}

func TestRunLossAndTruncateScenarios(t *testing.T) {
	lossCfg := testConfig(t, "loss")
	lossCfg.Losses = []int{0, 3, 4}
	lossCfg.Reorder = 2
	loss, err := Run(lossCfg)
	if err != nil {
		t.Fatal(err)
	}
	if loss.Metrics.UnrecoveredPackets != 3 || loss.Metrics.LossRecoveryRate != 0 {
		t.Fatalf("unexpected loss metrics: %+v", loss.Metrics)
	}
	if loss.Metrics.ReorderRecoveryRate != 1 {
		t.Fatalf("reorder should fully recover: %+v", loss.Metrics)
	}

	corruptCfg := testConfig(t, "corrupt")
	corruptCfg.Corrupt = "truncate"
	corrupt, err := Run(corruptCfg)
	if err != nil {
		t.Fatalf("recoverable truncation should exit successfully: %v", err)
	}
	if corrupt.Corruption == nil || corrupt.Corruption.Category != model.CategoryTruncatedBox || corrupt.Corruption.Offset <= 0 {
		t.Fatalf("missing truncation evidence: %+v", corrupt.Corruption)
	}
	if corrupt.Status != "ok_with_warnings" || len(corrupt.Warnings) == 0 {
		t.Fatalf("expected warning status: %s %+v", corrupt.Status, corrupt.Warnings)
	}
}
