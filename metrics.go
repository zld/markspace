package mediakernel

import "math"

// BitrateSpec compares a measured bitrate against its target and records the
// relative deviation. All formulas in this file are fixed and covered by tests
// so results are reproducible across runs.
type BitrateSpec struct {
	AssetID          string  `json:"asset_id"`
	TargetBps        float64 `json:"target_bps"`
	ActualBps        float64 `json:"actual_bps"`
	RelativeDeviation float64 `json:"relative_deviation"`
	Tolerance        float64 `json:"tolerance"`
	Pass             bool    `json:"pass"`
}

// BitrateSpecCheck computes (actual-target)/target and marks pass when the
// absolute deviation is within tolerance (fraction, e.g. 0.05 = 5%). A zero
// target with a non-zero actual is an automatic failure.
func BitrateSpecCheck(assetID string, targetBps, actualBps, tolerance float64) BitrateSpec {
	s := BitrateSpec{AssetID: assetID, TargetBps: targetBps, ActualBps: actualBps, Tolerance: tolerance}
	switch {
	case targetBps == 0 && actualBps == 0:
		s.RelativeDeviation = 0
		s.Pass = true
	case targetBps == 0:
		s.RelativeDeviation = math.Inf(1)
		s.Pass = false
	default:
		s.RelativeDeviation = (actualBps - targetBps) / targetBps
		s.Pass = math.Abs(s.RelativeDeviation) <= tolerance
	}
	return s
}

// EffectiveBps derives bitrate from total payload bytes and duration ticks on
// the given clock: bytes*8 / duration_seconds.
func EffectiveBps(totalBytes int64, durationTicks int64, clock Clock) float64 {
	if durationTicks <= 0 {
		return 0
	}
	seconds := float64(durationTicks) / float64(clock.effectiveRate())
	if seconds <= 0 {
		return 0
	}
	return float64(totalBytes*8) / seconds
}

// QualityResult reports an objective quality metric. Lossy processing carries
// a reproducible SNR proxy in dB; unprocessed/skipped assets report
// applicable=false with a machine-readable reason instead of a fabricated
// score.
type QualityResult struct {
	AssetID      string   `json:"asset_id"`
	Processed    bool     `json:"processed"`
	Applicable   bool     `json:"applicable"`
	SNRProxyDB   *float64 `json:"snr_proxy_db,omitempty"`
	Reason       string   `json:"reason,omitempty"`
	SignalEnergy float64  `json:"signal_energy"`
	NoiseEnergy  float64  `json:"noise_energy"`
}

// SNRProxy computes a deterministic SNR proxy in dB:
//
//	10 * log10( signalEnergy / noiseEnergy )
//
// signal/noise are caller-measured energy sums over matching sample windows,
// so the same inputs always yield the same score. Zero or negative noise
// energy (no processing error at all) is not a valid SNR and is reported as
// not applicable rather than +Inf.
func SNRProxy(assetID string, processed bool, signalEnergy, noiseEnergy float64) QualityResult {
	q := QualityResult{AssetID: assetID, Processed: processed, SignalEnergy: signalEnergy, NoiseEnergy: noiseEnergy}
	if !processed {
		q.Applicable = false
		q.Reason = "asset not processed: reused from prior index"
		return q
	}
	if noiseEnergy <= 0 {
		if signalEnergy <= 0 {
			q.Applicable = false
			q.Reason = "zero signal and noise energy"
			return q
		}
		q.Applicable = false
		q.Reason = "zero noise energy: lossless path"
		return q
	}
	snr := 10 * math.Log10(signalEnergy/noiseEnergy)
	q.Applicable = true
	q.SNRProxyDB = &snr
	return q
}

// SubtitleAlignment compares one subtitle interval against its reference audio
// interval on the shared media clock. Errors are exported in milliseconds.
type SubtitleAlignment struct {
	SubtitleID      string  `json:"subtitle_id"`
	StartErrorMs    float64 `json:"start_error_ms"`
	EndErrorMs      float64 `json:"end_error_ms"`
	AbsMaxErrorMs   float64 `json:"abs_max_error_ms"`
	SourceDigest    Digest  `json:"source_digest"`
	ToleranceMs     float64 `json:"tolerance_ms"`
	Pass            bool    `json:"pass"`
}

// CheckSubtitleAlignment maps both intervals through clock and verifies the
// subtitle start/end against the audio range. A failed item still records the
// measured error and carries the source digest for traceability.
func CheckSubtitleAlignment(id string, audio, subtitle TimeRange, clock Clock, toleranceMs float64, source Digest) SubtitleAlignment {
	startErr := clock.TicksToMillis(subtitle.StartTicks - audio.StartTicks)
	endErr := clock.TicksToMillis(subtitle.EndTicks - audio.EndTicks)
	absMax := math.Max(math.Abs(startErr), math.Abs(endErr))
	return SubtitleAlignment{
		SubtitleID:    id,
		StartErrorMs:  startErr,
		EndErrorMs:    endErr,
		AbsMaxErrorMs: absMax,
		SourceDigest:  source,
		ToleranceMs:   toleranceMs,
		Pass:          absMax <= toleranceMs,
	}
}

// AlignmentSummary aggregates per-subtitle results into max and average error.
type AlignmentSummary struct {
	Checked      int     `json:"checked"`
	Failed       int     `json:"failed"`
	MaxErrorMs   float64 `json:"max_error_ms"`
	AverageErrorMs float64 `json:"average_error_ms"`
}

// SummarizeAlignment aggregates the absolute max errors of each item.
func SummarizeAlignment(items []SubtitleAlignment) AlignmentSummary {
	s := AlignmentSummary{}
	var sum float64
	for _, it := range items {
		s.Checked++
		if !it.Pass {
			s.Failed++
		}
		sum += it.AbsMaxErrorMs
		if it.AbsMaxErrorMs > s.MaxErrorMs {
			s.MaxErrorMs = it.AbsMaxErrorMs
		}
	}
	if s.Checked > 0 {
		s.AverageErrorMs = sum / float64(s.Checked)
	}
	return s
}

// CorruptPacketRecord is the quantified record of a damaged packet in output.
type CorruptPacketRecord struct {
	Offset int64   `json:"offset"`
	Seq    *uint16 `json:"seq,omitempty"`
	Phase  string  `json:"phase"`
	Reason string  `json:"reason"`
}
