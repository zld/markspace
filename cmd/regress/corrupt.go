package main

import (
	"context"
	"fmt"
	"os"
	"path/filepath"

	"regress/internal/fixture"
	"regress/internal/pipeline"
	"regress/internal/scan"
)

// printMetrics renders the canonical terminal metrics. Zero-valued recovery
// counters are always printed so the output shape is stable across scenarios.
func printMetrics(m *pipeline.Metrics) {
	if m == nil {
		return
	}
	fmt.Printf("roundtrip=%.2f%%\n", m.RoundTripPct)
	if m.KeyframeHitPct > 0 {
		fmt.Printf("keyframe_hit=%.2f%%\n", m.KeyframeHitPct)
	}
	if m.HashMatch != "" {
		fmt.Printf("segment_hash_match=%s\n", m.HashMatch)
	}
	fmt.Printf("ssim_min=%.4f\n", m.SSIMMin)
	fmt.Printf("bitrate_peak_bps=%d\n", m.BitratePeakBPS)
	if m.Rejected > 0 {
		fmt.Printf("rejected=%d\n", m.Rejected)
	}
	// Recovery counters appear on every run for a stable, greppable shape.
	fmt.Printf("recovered=%d\n", m.Recovered)
	fmt.Printf("published=%d\n", m.Published)
	fmt.Printf("duplicate=%d\n", m.Duplicate)
	fmt.Printf("exit=%d\n", m.ExitCode)
}

// corruptOutcome generates the stream, rewrites one of four keyframe index
// entries to a non-keyframe offset, then validates and reports the hit rate.
func corruptOutcome(ctx context.Context, opt pipeline.Options) (*corruptResult, error) {
	fixDir := filepath.Join(opt.RootDir, "fixture", opt.Codec)
	if err := os.MkdirAll(fixDir, 0o755); err != nil {
		return nil, err
	}
	spec := fixture.DefaultSpec(opt.Codec)
	if opt.Segments > 0 {
		spec.Frames = opt.Segments * spec.GOP
	}
	set, err := fixture.Build(ctx, fixDir, spec)
	if err != nil {
		return nil, err
	}
	corruptPath, err := fixture.CorruptIndex(set, 1)
	if err != nil {
		return nil, err
	}
	f, err := os.Open(corruptPath)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	idx, err := scan.UnmarshalKeyIndex(f)
	if err != nil {
		return nil, err
	}
	report, _ := scan.ValidateIndex(set.Scan, idx)
	return &corruptResult{
		hitPct:   100 * float64(report.Accepted) / float64(report.Total),
		rejected: report.Rejected,
	}, nil
}

type corruptResult struct {
	hitPct   float64
	rejected int
}

// runCorruptIndex builds the stream, damages one of four index entries and
// reports the keyframe hit rate without publishing any manifest.
func runCorruptIndex(ctx context.Context, opt pipeline.Options) int {
	res, err := corruptOutcome(ctx, opt)
	if err != nil {
		fmt.Fprintln(os.Stderr, "error:", err)
		return 1
	}
	code := 0
	if res.rejected > 0 {
		code = 2
	}
	fmt.Printf("keyframe_hit=%.2f%%\n", res.hitPct)
	fmt.Printf("rejected=%d\n", res.rejected)
	fmt.Println("published=0")
	fmt.Printf("exit=%d\n", code)
	return code
}
