package analyze

import (
	"mediamux/media"
)

// QualitySample is one segment's contribution to the sliding window.
type QualitySample struct {
	Index      int   `json:"index"`
	Bytes      int64 `json:"bytes"`
	DurationMS int64 `json:"duration_ms"`
	KeyframeMS int64 `json:"keyframe_ms"`
	CorruptMS  int64 `json:"corrupt_ms"`
	Window     int   `json:"window"`
}

// QualitySummary is the terminal-facing statistics block. Every value is
// derived from local samples only; no clock or network input is consulted.
type QualitySummary struct {
	TargetBPS           int64   `json:"target_bps"`
	ActualBPS           int64   `json:"actual_bps"`
	BitrateDeviationPct float64 `json:"bitrate_deviation_pct"`
	WindowPeakBPS       int64   `json:"window_peak_bps"`
	WindowLowBPS        int64   `json:"window_low_bps"`
	WindowCount         int     `json:"window_count"`
	QualitySamples      int     `json:"quality_samples"`
	MediaBytes          int64   `json:"media_bytes"`
	DurationMS          int64   `json:"duration_ms"`
	KeyframeGapP50MS    int64   `json:"keyframe_gap_p50_ms"`
	KeyframeGapP95MS    int64   `json:"keyframe_gap_p95_ms"`
	KeyframeCount       int     `json:"keyframe_count"`
	CorruptSegments     int     `json:"corrupt_segments"`
	CorruptDurationMS   int64   `json:"corrupt_duration_ms"`
	RTPDuplicates       int     `json:"rtp_duplicates"`
	RTPMissing          int     `json:"rtp_missing"`
	RTPMissingMS        int64   `json:"rtp_missing_ms"`
	AlignmentErrorMS    int64   `json:"manifest_alignment_error_ms"`
	AlignmentMaxMS      int64   `json:"manifest_alignment_max_ms"`
}

// QualityOptions configures the sliding-window rate estimator.
type QualityOptions struct {
	// Window is the number of segments averaged for peak/low bitrate. Zero
	// uses 3, matching a short sliding window at typical segment lengths.
	Window int
	// TargetBPS is the manifest-declared rate.
	TargetBPS int64
	// AlignmentMS is the accumulated manifest alignment error.
	AlignmentMS int64
	// AlignmentMaxMS is the largest single alignment residual.
	AlignmentMaxMS int64
	// PacketDurationMS is the transport share of each RTP gap, used to turn
	// missing packets into missing media time.
	PacketDurationMS int64
}

// ComputeQuality folds per-segment observations into the summary. Bitrates
// are integer maths; only the reported percentage is a float, and it comes
// from a single division at the end so repeated runs are identical.
func ComputeQuality(samples []QualitySample, gaps []int64, rtp *RTPResult, opts QualityOptions) QualitySummary {
	window := opts.Window
	if window <= 0 {
		window = 3
	}
	sum := QualitySummary{
		TargetBPS:        opts.TargetBPS,
		QualitySamples:   len(samples),
		AlignmentErrorMS: opts.AlignmentMS,
		AlignmentMaxMS:   opts.AlignmentMaxMS,
	}
	for _, s := range samples {
		sum.MediaBytes += s.Bytes
		sum.DurationMS += s.DurationMS
		sum.CorruptDurationMS += s.CorruptMS
		if s.CorruptMS > 0 {
			sum.CorruptSegments++
		}
	}
	if sum.DurationMS > 0 {
		sum.ActualBPS = sum.MediaBytes * 8 * 1000 / sum.DurationMS
	}
	if sum.TargetBPS > 0 {
		dev := float64(sum.ActualBPS-sum.TargetBPS) * 100 / float64(sum.TargetBPS)
		sum.BitrateDeviationPct = roundTo(dev, 2)
	}
	// Sliding window: each window's rate uses its own duration so unequal
	// segment lengths do not skew the estimate.
	for i := 0; i+window <= len(samples); i++ {
		var bytes, dur int64
		for j := i; j < i+window; j++ {
			bytes += samples[j].Bytes
			dur += samples[j].DurationMS
		}
		if dur <= 0 {
			continue
		}
		bps := bytes * 8 * 1000 / dur
		if sum.WindowPeakBPS == 0 || bps > sum.WindowPeakBPS {
			sum.WindowPeakBPS = bps
		}
		if sum.WindowLowBPS == 0 || bps < sum.WindowLowBPS {
			sum.WindowLowBPS = bps
		}
		sum.WindowCount++
	}
	sum.KeyframeGapP50MS = Percentile(gaps, 0.5)
	sum.KeyframeGapP95MS = Percentile(gaps, 0.95)
	sum.KeyframeCount = len(gaps)
	if rtp != nil {
		sum.RTPDuplicates = rtp.Duplicates
		sum.RTPMissing = rtp.Missing
		if opts.PacketDurationMS > 0 {
			sum.RTPMissingMS = int64(rtp.Missing) * opts.PacketDurationMS
		}
	}
	return sum
}

// roundTo rounds to n decimal places using integer scaling so identical
// inputs always produce identical output.
func roundTo(v float64, places int) float64 {
	scale := 1.0
	for i := 0; i < places; i++ {
		scale *= 10
	}
	if v < 0 {
		return -float64(int64(-v*scale+0.5)) / scale
	}
	return float64(int64(v*scale+0.5)) / scale
}

// SegmentSample derives a quality sample from one analysed segment.
func SegmentSample(res *SegmentResult) QualitySample {
	durMS := scaleTicks(res.Duration.Value, media.Timescale90K, media.TimescaleMS)
	if durMS == 0 {
		// A segment whose index never parsed still carried bytes; give it the
		// smallest positive duration so it is not discounted as free.
		durMS = 1
	}
	var corruptMS int64
	if res.Corrupt {
		// The whole segment is untrusted, so all of its duration is damaged
		// media time rather than a byte-count proxy.
		corruptMS = durMS
	}
	return QualitySample{
		Index:      res.Index,
		Bytes:      res.Bytes,
		DurationMS: durMS,
		CorruptMS:  corruptMS,
	}
}
