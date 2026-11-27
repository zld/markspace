package audio

import (
	"math"

	merr "medialib/internal/errors"
)

// LoudnessStats reports what loudness control measured and applied.
type LoudnessStats struct {
	InputRMSDBFS  float64
	GainDB        float64
	PeakDBFS      float64 // peak after gain and any limiting
	LimiterEngaged bool   // true when the peak ceiling forced extra gain down
}

// Normalize applies gain so the output RMS meets targetRMSDBFS, then
// enforces the peak ceiling: if the gained signal would exceed
// peakCeilDBFS the gain is reduced to the ceiling and the overrun is
// reported through LimiterEngaged. Silence is an error, not zero gain.
func Normalize(in PCM, targetRMSDBFS, peakCeilDBFS float64) (PCM, LoudnessStats, error) {
	var st LoudnessStats
	if len(in.Samples) == 0 {
		return PCM{}, st, merr.Newf(merr.StageLoudness, -1, "normalize", "empty input")
	}
	var sum float64
	for _, s := range in.Samples {
		sum += float64(s) * float64(s)
	}
	rms := math.Sqrt(sum / float64(len(in.Samples)))
	if rms == 0 {
		return PCM{}, st, merr.Newf(merr.StageLoudness, -1, "normalize",
			"input is digital silence")
	}
	st.InputRMSDBFS = 20 * math.Log10(rms)
	gainDB := targetRMSDBFS - st.InputRMSDBFS
	// Predict the peak after gain; pull back to the ceiling if needed.
	peak := 0.0
	for _, s := range in.Samples {
		if a := math.Abs(float64(s)); a > peak {
			peak = a
		}
	}
	peakAfter := 20*math.Log10(peak) + gainDB
	if peakAfter > peakCeilDBFS {
		gainDB -= peakAfter - peakCeilDBFS
		st.LimiterEngaged = true
	}
	st.GainDB = gainDB
	g := float32(math.Pow(10, gainDB/20))
	out := PCM{Rate: in.Rate, Channels: in.Channels, Samples: make([]float32, len(in.Samples))}
	outPeak := 0.0
	for i, s := range in.Samples {
		v := clamp(s * g)
		out.Samples[i] = v
		if a := math.Abs(float64(v)); a > outPeak {
			outPeak = a
		}
	}
	if outPeak == 0 {
		st.PeakDBFS = math.Inf(-1)
	} else {
		st.PeakDBFS = 20 * math.Log10(outPeak)
	}
	return out, st, nil
}
