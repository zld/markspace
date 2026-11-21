// Package quality computes frame-aligned PSNR between source and encoded
// frames and summarizes per-segment and run-level statistics with Gonum.
package quality

import (
	"context"
	"fmt"
	"math"

	"gonum.org/v1/gonum/stat"

	"bitrack/internal/media"
	"bitrack/internal/model"
)

// identicalPSNR is the conventional placeholder reported when MSE is zero.
const identicalPSNR = 99.0

// CompareResult holds frame- and segment-level quality numbers.
type CompareResult struct {
	FramePSNR []float64
	MeanDB    float64
	StdDevDB  float64
}

// CompareFrames requires two equal-length, equal-dimension frame sets aligned
// by frame index. A frame-count or dimension mismatch is an error rather than
// a silent skip, because misaligned PSNR is meaningless.
func CompareFrames(src, dst [][]byte, width, height int) (CompareResult, error) {
	if len(src) != len(dst) {
		return CompareResult{}, fmt.Errorf(
			"psnr: frame count mismatch: source %d frames vs output %d frames", len(src), len(dst))
	}
	if len(src) == 0 {
		return CompareResult{}, fmt.Errorf("psnr: no frames to compare")
	}
	expected := width * height * 3 / 2
	framePSNR := make([]float64, len(src))
	for i := range src {
		if len(src[i]) != expected || len(dst[i]) != expected {
			return CompareResult{}, fmt.Errorf(
				"psnr: frame %d dimension mismatch (want %d yuv420p bytes, got %d/%d)",
				i, expected, len(src[i]), len(dst[i]))
		}
		framePSNR[i] = framePSNRY(src[i], dst[i], width, height)
	}
	mean, variance := stat.MeanVariance(framePSNR, nil)
	return CompareResult{
		FramePSNR: framePSNR,
		MeanDB:    mean,
		StdDevDB:  math.Sqrt(variance),
	}, nil
}

// framePSNRY computes PSNR over the luma (Y) plane only. U/V are neutral gray
// in the fixture pipeline and would only dilute the signal.
func framePSNRY(a, b []byte, w, h int) float64 {
	n := w * h
	var se float64
	for i := 0; i < n; i++ {
		d := float64(a[i]) - float64(b[i])
		se += d * d
	}
	mse := se / float64(n)
	if mse == 0 {
		return identicalPSNR
	}
	return 10 * math.Log10(255.0*255.0/mse)
}

// SegmentMean is a convenience for callers that already hold frame values.
func SegmentMean(framePSNR []float64) float64 {
	if len(framePSNR) == 0 {
		return 0
	}
	return stat.Mean(framePSNR, nil)
}

// Aggregate computes run-level mean and standard deviation across segment
// PSNR values, and counts regressions against the baseline. A regression is a
// segment whose baseline-relative drop exceeds maxDropDB.
func Aggregate(segments []*model.Segment, maxDropDB float64) (mean, stddev float64, regressions int) {
	if len(segments) == 0 {
		return 0, 0, 0
	}
	vals := make([]float64, len(segments))
	for i, s := range segments {
		vals[i] = s.PSNRDB
		// Delta is stored as current-baseline; a drop is therefore a negative
		// delta whose magnitude exceeds the allowed threshold.
		if s.BaselinePSNR > 0 && s.BaselinePSNR-s.PSNRDB > maxDropDB {
			regressions++
		}
	}
	m, v := stat.MeanVariance(vals, nil)
	return m, math.Sqrt(v), regressions
}

// VerifyFrameCounts re-counts frames in both files and refuses to proceed on
// any mismatch; used before the full PSNR pass as an early hard failure.
func VerifyFrameCounts(ctx context.Context, srcPath, dstPath string) error {
	sn, err := media.ProbeFrameCount(ctx, srcPath)
	if err != nil {
		return err
	}
	dn, err := media.ProbeFrameCount(ctx, dstPath)
	if err != nil {
		return err
	}
	if sn != dn {
		return fmt.Errorf("psnr: total frame count mismatch: source %d vs output %d", sn, dn)
	}
	return nil
}
