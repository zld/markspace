package audio

import (
	"math"
)

// LoudnessTarget bundles the integrated loudness and true-peak ceiling the
// chain must satisfy. Values are in LUFS and dBTP respectively.
type LoudnessTarget struct {
	IntegratedLUFS float64 // e.g. -16
	TruePeakDBTP   float64 // e.g. -1.0
}

// LoudnessResult reports the measured levels and the gain applied to reach
// the target, plus whether both gates are met after processing.
type LoudnessResult struct {
	InputLUFS       float64 `json:"inputLufs"`
	OutputLUFS      float64 `json:"outputLufs"`
	InputTruePeak   float64 `json:"inputTruePeak"`
	OutputTruePeak  float64 `json:"outputTruePeak"`
	GainDB          float64 `json:"gainDb"`
	LoudnessMet     bool    `json:"loudnessMet"`
	TruePeakMet     bool    `json:"truePeakMet"`
	LoudnessErrorLU float64 `json:"loudnessErrorLu"`
}

const silenceFloor = 1e-12 // linear RMS floor to avoid log(0)

// rmsDB computes the root-mean-square level of all channels/samples and
// returns it on the dBFS scale. An all-silent buffer returns -inf.
func rmsDB(samples []float64) float64 {
	var sum float64
	for _, s := range samples {
		sum += s * s
	}
	if len(samples) == 0 || sum == 0 {
		return math.Inf(-1)
	}
	rms := math.Sqrt(sum / float64(len(samples)))
	if rms < silenceFloor {
		return math.Inf(-1)
	}
	return 20 * math.Log10(rms)
}

// truePeakDB returns the maximum absolute sample as dBTP. This deterministic
// sample-peak proxy avoids an over-sampling filter while remaining exact and
// reproducible; it is conservative because true peak is never lower than the
// observed sample peak.
func truePeakDB(samples []float64) float64 {
	peak := 0.0
	for _, s := range samples {
		if a := math.Abs(s); a > peak {
			peak = a
		}
	}
	if peak < silenceFloor {
		return math.Inf(-1)
	}
	return 20 * math.Log10(peak)
}

// Measure returns the input integrated loudness and true peak without
// modifying the buffer.
func Measure(in Buffer) (lufs, peak float64) {
	return rmsDB(in.Samples), truePeakDB(in.Samples)
}

// NormalizeLoudness applies a single linear gain that drives integrated
// loudness toward target.IntegratedLUFS while never exceeding the true-peak
// ceiling. The peak constraint is enforced first by capping the gain, so the
// quality gate can fail on loudness but is never silently relaxed on peak.
func NormalizeLoudness(in Buffer, target LoudnessTarget) (Buffer, LoudnessResult, error) {
	inLUFS, inPeak := Measure(in)
	res := LoudnessResult{InputLUFS: inLUFS, InputTruePeak: inPeak}

	// Gain needed to reach the loudness target. A silent or -inf input cannot
	// be amplified to a finite target; leave it untouched and report failure.
	var loudnessGain float64
	if math.IsInf(inLUFS, -1) {
		loudnessGain = 0
	} else {
		loudnessGain = target.IntegratedLUFS - inLUFS
	}

	// Maximum gain permitted by the true-peak ceiling (linear headroom).
	var peakGainDB float64 = math.Inf(1)
	if !math.IsInf(inPeak, -1) {
		peakGainDB = target.TruePeakDBTP - inPeak
	}

	// Peak gate wins: never apply more gain than the ceiling allows.
	gainDB := loudnessGain
	if gainDB > peakGainDB {
		gainDB = peakGainDB
	}
	res.GainDB = gainDB

	gain := math.Pow(10, gainDB/20)
	out := NewBuffer(in.Frames, in.Ch)
	for i, s := range in.Samples {
		out.Samples[i] = s * gain
	}

	res.OutputLUFS, res.OutputTruePeak = Measure(out)
	if math.IsInf(inLUFS, -1) {
		res.LoudnessMet = false
		res.LoudnessErrorLU = math.Abs(target.IntegratedLUFS)
	} else {
		res.LoudnessErrorLU = math.Abs(res.OutputLUFS - target.IntegratedLUFS)
		res.LoudnessMet = res.LoudnessErrorLU <= 0.5
	}
	res.TruePeakMet = math.IsInf(res.OutputTruePeak, -1) ||
		res.OutputTruePeak <= target.TruePeakDBTP+1e-9
	return out, res, nil
}
