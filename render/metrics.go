package render

import "math"

// QualityStats summarizes per-frame pixel distribution.
type QualityStats struct {
	Mean     float64 // mean of all RGBA channel values 0..255
	Variance float64
}

// BitrateReport compares target and achieved bitrate.
type BitrateReport struct {
	TargetBPS    int64
	ActualBPS    int64
	DeltaPercent float64 // signed deviation magnitude target, e.g. -2.1 means 2.1% under
}

// FrameMetrics bundles everything recorded per frame in the manifest.
type FrameMetrics struct {
	Index        int64
	TimeNS       int64
	SHA256       string
	Bytes        int
	Quality      QualityStats
	BitrateBPS   int64
	Bad          bool
	FirstBadTime int64
}

// PixelStats computes mean and population variance over RGBA channels.
func PixelStats(pix []byte) QualityStats {
	if len(pix) == 0 {
		return QualityStats{}
	}
	var sum float64
	for _, v := range pix {
		sum += float64(v)
	}
	mean := sum / float64(len(pix))
	var acc float64
	for _, v := range pix {
		d := float64(v) - mean
		acc += d * d
	}
	return QualityStats{Mean: mean, Variance: acc / float64(len(pix))}
}

// Measure builds the per-frame metrics record. frameBytes is the encoded
// sample size used for bitrate accounting.
func Measure(f Frame, frameBytes int, targetBPS int64, intervalNS int64) FrameMetrics {
	stats := PixelStats(f.Pix)
	bps := int64(0)
	if intervalNS > 0 {
		// bytes*8 / seconds; seconds = intervalNS/1e9.
		bps = int64(math.Round(float64(frameBytes) * 8 * 1e9 / float64(intervalNS)))
	}
	return FrameMetrics{
		Index:        f.Index,
		TimeNS:       f.TimeNS,
		SHA256:       FrameHash(f),
		Bytes:        frameBytes,
		Quality:      stats,
		BitrateBPS:   bps,
		Bad:          f.Bad,
		FirstBadTime: f.BadTime,
	}
}

// AggregateBitrate computes whole-sequence actual bitrate from total bytes and
// duration, then the signed percentage deviation from target.
func AggregateBitrate(totalBytes int64, durationNS int64, targetBPS int64) BitrateReport {
	actual := int64(0)
	if durationNS > 0 {
		actual = int64(math.Round(float64(totalBytes) * 8 * 1e9 / float64(durationNS)))
	}
	delta := 0.0
	if targetBPS > 0 {
		delta = (float64(actual-targetBPS) / float64(targetBPS)) * 100
	}
	return BitrateReport{TargetBPS: targetBPS, ActualBPS: actual, DeltaPercent: delta}
}

// WithinThreshold reports whether |delta| <= threshold percent.
func (b BitrateReport) WithinThreshold(thresholdPercent float64) bool {
	return math.Abs(b.DeltaPercent) <= thresholdPercent
}

// PlanBitrate derives the per-frame byte budget that hits targetBPS at fps.
// Frame sizes are chosen from this budget so actual stays within 5%.
func PlanBitrate(targetBPS int64, fps int) int {
	if fps <= 0 || targetBPS <= 0 {
		return 0
	}
	return int(math.Round(float64(targetBPS) / 8.0 / float64(fps)))
}
