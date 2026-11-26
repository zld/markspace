package verify_test

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"regress/internal/fault"
	"regress/internal/fixture"
	"regress/internal/plan"
	"regress/internal/publish"
	"regress/internal/scan"
	"regress/internal/verify"
	"regress/internal/worker"
)

// buildPublished runs the real planner+publisher path to produce a valid
// artifact set rooted in a temp directory.
func buildPublished(t *testing.T, codec string) (spec fixture.Spec, sourcePath, jobRoot string) {
	t.Helper()
	dir := t.TempDir()
	spec = fixture.DefaultSpec(codec)
	set, err := fixture.Build(context.Background(), filepath.Join(dir, "src"), spec)
	if err != nil {
		t.Fatal(err)
	}
	seg := plan.BuildSegmentPlan(set.Scan, set.TimeDen)
	enc := &worker.FFmpegEncoder{
		SourcePath: set.StreamPath, Codec: spec.Codec, WorkDir: filepath.Join(dir, "work"),
		Segments: seg, SourceW: spec.Width, SourceH: spec.Height, SourceFPS: spec.FPS,
	}
	con := plan.Constraints{SSIMMin: 0.90, MaxBitrateBPS: 6_000_000, WorkBudget: 200000, Timescale: 24}
	dec, err := plan.NewPlanner(enc, spec.Codec, set.Scan, con).Run(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	pub, err := publish.NewPublisher(filepath.Join(dir, "stage"), filepath.Join(dir, "out"))
	if err != nil {
		t.Fatal(err)
	}
	art, err := pub.Publish(context.Background(), &publish.Request{
		JobKey: "job", Codec: spec.Codec, SourcePath: set.StreamPath,
		SourceW: spec.Width, SourceH: spec.Height, SourceFPS: spec.FPS, Decision: dec,
	})
	if err != nil {
		t.Fatal(err)
	}
	return spec, set.StreamPath, filepath.Dir(art.ManifestPath)
}

func TestVerifyCleanArtifacts(t *testing.T) {
	spec, source, root := buildPublished(t, scan.CodecH264)
	ssim, err := verify.MeasureSSIM(context.Background(), scan.CodecH264,
		source, source, spec.Width, spec.Height, spec.FPS)
	if err != nil {
		t.Fatal(err)
	}
	if ssim < 0.99 {
		t.Fatalf("self-vs-self SSIM %.4f unexpectedly low", ssim)
	}
	rep, err := verify.NewChecker(root).Check(context.Background(), verify.Expectations{
		SSIMMin: 0.90, MaxBitrateBPS: 6_000_000, MeasuredSSIM: ssim,
	})
	if err != nil {
		t.Fatalf("clean artifacts should verify: %v (%v)", err, rep.Failures)
	}
	if rep.RoundTripPct != 100 {
		t.Fatalf("round trip = %.2f%%, want 100", rep.RoundTripPct)
	}
	if rep.HashesOK != rep.SegmentsChecked || rep.SegmentsChecked < 1 {
		t.Fatalf("hash coverage %d/%d", rep.HashesOK, rep.SegmentsChecked)
	}
	if !rep.BoundaryOK || !rep.AuditOK {
		t.Fatalf("boundary=%v audit=%v", rep.BoundaryOK, rep.AuditOK)
	}
}

func TestVerifyDetectsTamperedSegment(t *testing.T) {
	_, _, root := buildPublished(t, scan.CodecH264)
	manifest, err := publish.ReadManifest(filepath.Join(root, "manifest.json"))
	if err != nil {
		t.Fatal(err)
	}
	// Corrupt one segment's bytes without updating the manifest hash.
	target := filepath.Join(root, manifest.Segments[0].File)
	raw, err := os.ReadFile(target)
	if err != nil {
		t.Fatal(err)
	}
	raw[len(raw)-1] ^= 0xFF
	if err := os.WriteFile(target, raw, 0o644); err != nil {
		t.Fatal(err)
	}
	_, err = verify.NewChecker(root).Check(context.Background(), verify.Expectations{
		SSIMMin: 0.90, MaxBitrateBPS: 6_000_000, MeasuredSSIM: 0.99,
	})
	if err == nil || !fault.HasCode(err, fault.CodeHashMismatch) {
		t.Fatalf("want hash-mismatch, got %v", err)
	}
}

func TestVerifyRejectsLowSSIMAndBitrate(t *testing.T) {
	_, _, root := buildPublished(t, scan.CodecH264)
	// Independent SSIM below the floor must be rejected regardless of planner.
	_, err := verify.NewChecker(root).Check(context.Background(), verify.Expectations{
		SSIMMin: 0.99, MaxBitrateBPS: 6_000_000, MeasuredSSIM: 0.80,
	})
	if err == nil || !fault.HasCode(err, fault.CodeQualityBelowMin) {
		t.Fatalf("want quality rejection, got %v", err)
	}
	// A zero bitrate ceiling must trip the independent bitrate check.
	rep, err := verify.NewChecker(root).Check(context.Background(), verify.Expectations{
		SSIMMin: 0.80, MaxBitrateBPS: 1, MeasuredSSIM: 0.95,
	})
	if err == nil || !fault.HasCode(err, fault.CodeBitrateAboveMax) {
		t.Fatalf("want bitrate rejection, got %v", err)
	}
	if rep.BitratePeakBPS <= 0 {
		t.Fatal("verifier did not compute a peak bitrate")
	}
}

func TestVerifyRejectsMissingSegment(t *testing.T) {
	_, _, root := buildPublished(t, scan.CodecH264)
	manifest, _ := publish.ReadManifest(filepath.Join(root, "manifest.json"))
	if err := os.Remove(filepath.Join(root, manifest.Segments[1].File)); err != nil {
		t.Fatal(err)
	}
	_, err := verify.NewChecker(root).Check(context.Background(), verify.Expectations{
		SSIMMin: 0.80, MaxBitrateBPS: 6_000_000, MeasuredSSIM: 0.95,
	})
	if err == nil {
		t.Fatal("expected failure for manifest-referenced missing segment")
	}
}
