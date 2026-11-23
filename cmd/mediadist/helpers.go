package main

import (
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"mediadist/pipeline"
	"mediadist/segment"
	"mediadist/timeline"
)

// ensureWritableDir creates the output directory and rejects traversal.
func ensureWritableDir(dir string) error {
	clean := filepath.Clean(dir)
	if strings.HasPrefix(clean, "..") || filepath.IsAbs(clean) == false && strings.Contains(clean, "..") {
		return timeline.NewLocError(timeline.CodeBadArgument, pipeline.StageManifest, 0, -1,
			"path", errors.New("relative path traversal rejected"))
	}
	if err := os.MkdirAll(clean, 0o755); err != nil {
		return timeline.NewLocError("IO_ERROR", pipeline.StageManifest, 0, -1, "mkdir", err)
	}
	return nil
}

// safeJoin confines child paths to dir, mirroring the checkpoint guard.
func safeJoin(dir, name string) (string, error) {
	absRoot, err := filepath.Abs(filepath.Clean(dir))
	if err != nil {
		return "", err
	}
	target := filepath.Clean(filepath.Join(absRoot, name))
	rel, err := filepath.Rel(absRoot, target)
	if err != nil || rel == ".." || strings.HasPrefix(rel, ".."+string(os.PathSeparator)) {
		return "", timeline.NewLocError(timeline.CodeBadArgument, pipeline.StageManifest, 0, -1,
			"path", fmt.Errorf("path %q escapes %q", name, dir))
	}
	return target, nil
}

func parseInputDir(args []string) (string, error) {
	fs := flag.NewFlagSet("verify", flag.ContinueOnError)
	input := fs.String("input", "", "published output directory (required)")
	if err := fs.Parse(args); err != nil {
		return "", err
	}
	if *input == "" {
		return "", errors.New("--input is required")
	}
	return *input, nil
}

func parseRecoverDirs(args []string) (string, string, error) {
	fs := flag.NewFlagSet("recover", flag.ContinueOnError)
	input := fs.String("input", "", "failing run directory (required)")
	out := fs.String("out", "", "recovered output directory (required)")
	if err := fs.Parse(args); err != nil {
		return "", "", err
	}
	if *input == "" || *out == "" {
		return "", "", errors.New("--input and --out are required")
	}
	return *input, *out, nil
}

// persistRequest writes the deterministic parameters so recovery can rebuild
// identical clean fixtures without any external files or network access.
func persistRequest(dir string, rec requestRecord) error {
	p, err := safeJoin(dir, "request.json")
	if err != nil {
		return err
	}
	raw, err := json.MarshalIndent(rec, "", "  ")
	if err != nil {
		return err
	}
	tmp := p + ".tmp"
	if err := os.WriteFile(tmp, raw, 0o644); err != nil {
		return err
	}
	return os.Rename(tmp, p)
}

// printSummary emits the exact acceptance fields to stdout.
func printSummary(r *pipeline.Result, outDir string) {
	m := r.Manifest
	fmt.Printf("output_dir=%s\n", outDir)
	fmt.Printf("segment_count=%d\n", m.SegmentCount)
	fmt.Printf("representations=%d\n", len(m.Representations))
	for _, rep := range m.Representations {
		fmt.Printf("representation=%s\n", rep.ID)
		fmt.Printf("  target_bitrate=%d\n", rep.TargetBitrateBps)
		fmt.Printf("  actual_bitrate=%d\n", rep.MeanActualBps)
		errPct := float64(rep.BitrateErrorBasisPts) / 100.0
		fmt.Printf("  bitrate_error_pct=%.2f\n", errPct)
		fmt.Printf("  audio_channels_in=%d\n", rep.AudioChannelsIn)
		fmt.Printf("  audio_channels_out=%d\n", rep.AudioChannelsOut)
		fmt.Printf("  sample_rate_in=%d\n", rep.SampleRateIn)
		fmt.Printf("  sample_rate_out=%d\n", rep.SampleRateOut)
		fmt.Printf("  color_source=%s\n", rep.ColorSource)
		fmt.Printf("  quality_psnr_db=%d.%02d\n", rep.QualityPSNRHunddB/100, rep.QualityPSNRHunddB%100)
	}
	fmt.Printf("manifest_duration_ms=%d\n", segment.DurationMs(m.ManifestDurationTicks))
	fmt.Printf("timeline_end_ms=%d\n", segment.DurationMs(m.TimelineEndTicks))
	fmt.Printf("alignment_error_ms=%d\n", segment.DurationMs(m.AlignmentErrorTicks))
	fmt.Printf("manifest_sha256=%s\n", m.SHA256)
}

func printManifestVerification(m *segment.Manifest) {
	fmt.Printf("segment_count=%d\n", m.SegmentCount)
	fmt.Printf("representations=%d\n", len(m.Representations))
	fmt.Printf("manifest_duration_ms=%d\n", segment.DurationMs(m.ManifestDurationTicks))
	fmt.Printf("timeline_end_ms=%d\n", segment.DurationMs(m.TimelineEndTicks))
	fmt.Printf("alignment_error_ms=%d\n", segment.DurationMs(m.AlignmentErrorTicks))
	fmt.Printf("manifest_sha256=%s\n", m.SHA256)
	fmt.Println("verify=OK")
}

func printRecoverySummary(r *pipeline.Result, outDir string, recoveryCount int,
	failedStage, recoveredStage string) {
	fmt.Printf("output_dir=%s\n", outDir)
	fmt.Printf("recovery_count=%d\n", recoveryCount)
	fmt.Printf("failed_stage=%s\n", failedStage)
	fmt.Printf("recovered_stage=%s\n", recoveredStage)
	fmt.Printf("alignment_error_ms=%d\n", segment.DurationMs(r.Manifest.AlignmentErrorTicks))
	fmt.Printf("segment_count=%d\n", r.Manifest.SegmentCount)
	fmt.Printf("manifest_duration_ms=%d\n", segment.DurationMs(r.Manifest.ManifestDurationTicks))
	fmt.Printf("timeline_end_ms=%d\n", segment.DurationMs(r.Manifest.TimelineEndTicks))
	fmt.Printf("manifest_sha256=%s\n", r.Manifest.SHA256)
	fmt.Println("recover=OK")
}
