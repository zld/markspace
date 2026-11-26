package audio

import "math"

// Loudness targets from the processing requirements.
const (
	TargetRMSDBFS = -18.0 // target effective RMS in dBFS
	PeakCeilingDB = -1.0  // no sample may exceed this peak in dBFS
)

// LoudnessReport is the measured result plus the decision recorded in
// the manifest. When the peak limiter engages, the actually achieved
// loudness is reported instead of the target.
type LoudnessReport struct {
	InputRMSDB    float64 `json:"input_rms_db"`
	OutputRMSDB   float64 `json:"output_rms_db"`
	InputPeakDB   float64 `json:"input_peak_db"`
	OutputPeakDB  float64 `json:"output_peak_db"`
	GainDB        float64 `json:"gain_db"`
	Limited       bool    `json:"peak_limited"`
	TargetRMSDB   float64 `json:"target_rms_db"`
	PeakCeilingDB float64 `json:"peak_ceiling_db"`
}

// NormalizeLoudness scales interleaved stereo PCM so its effective RMS
// reaches TargetRMSDBFS, then applies a hard peak limiter at
// PeakCeilingDB. The limiter is a per-sample ceiling: if plain RMS gain
// would push a sample above the ceiling, the gain is reduced to the
// largest value that keeps every sample within bounds. The returned
// report states the achieved loudness and whether limiting occurred.
func NormalizeLoudness(samples []float32) ([]float32, LoudnessReport) {
	rep := LoudnessReport{
		TargetRMSDB: TargetRMSDBFS, PeakCeilingDB: PeakCeilingDB,
		InputRMSDB: DBFS(RMS(samples)),
		InputPeakDB: DBFS(float64(Peak(samples))),
	}
	out := make([]float32, len(samples))
	copy(out, samples)
	if len(samples) == 0 {
		return out, rep
	}
	rms := RMS(samples)
	if rms <= 0 {
		// Silence: nothing to measure; leave untouched and report honestly.
		rep.OutputRMSDB = math.Inf(-1)
		rep.OutputPeakDB = math.Inf(-1)
		return out, rep
	}
	targetLinear := math.Pow(10, TargetRMSDBFS/20)
	rmsGain := targetLinear / rms

	// Peak-safe gain: ceilingLinear / current peak.
	peak := float64(Peak(samples))
	ceilingLinear := math.Pow(10, PeakCeilingDB/20)
	peakSafeGain := ceilingLinear / peak
	gain := rmsGain
	if gain > peakSafeGain {
		gain = peakSafeGain
		rep.Limited = true
	}
	rep.GainDB = DBFS(gain)
	for i, v := range samples {
		out[i] = float32(float64(v) * gain)
	}
	rep.OutputRMSDB = DBFS(RMS(out))
	rep.OutputPeakDB = DBFS(float64(Peak(out)))
	return out, rep
}

// VerifyLoudness checks an output block against the acceptance window:
// RMS within tolerance of target (unless the limiter forced a lower
// level) and peak never above the ceiling.
func VerifyLoudness(rep LoudnessReport, rmsTolerance float64) (pass bool, reason string) {
	if rep.OutputPeakDB > PeakCeilingDB+1e-9 {
		return false, "peak exceeds -1 dBFS ceiling"
	}
	if rep.Limited {
		// Limiter engaged: the achieved RMS is authoritative.
		return true, "peak-limited; achieved loudness reported"
	}
	if math.Abs(rep.OutputRMSDB-TargetRMSDBFS) > rmsTolerance {
		return false, "RMS outside tolerance window"
	}
	return true, "PASS"
}
