package audio

// Loudness analysis and gain control. The meter follows the EBU R128
// gating structure (400 ms blocks, 75 % overlap, -70 LUFS absolute gate and
// -10 LU relative gate) on channel power sums; the limiter is a
// deterministic sample-peak clamp.
import "math"

// LoudnessResult reports the measurements gathered from one PCM buffer.
type LoudnessResult struct {
	IntegratedLUFS float64
	PeakDBFS       float64
	Clipped        int
	Frames         int64
}

// Measure computes integrated loudness, sample peak and clipped sample count
// for interleaved PCM at the given rate.
func Measure(pcm []float64, channels, rate int) LoudnessResult {
	res := LoudnessResult{}
	if channels <= 0 || rate <= 0 || len(pcm) < channels {
		return res
	}
	res.Frames = int64(len(pcm) / channels)
	peak := 0.0
	for i, v := range pcm {
		a := math.Abs(v)
		if a > peak {
			peak = a
		}
		if a >= 1.0 {
			res.Clipped++
		}
		_ = i
	}
	res.PeakDBFS = dbfs(peak)

	block := rate * 4 / 10 // 400 ms
	step := block / 4      // 75 % overlap
	if block <= 0 || step <= 0 || res.Frames < int64(block) {
		res.IntegratedLUFS = -math.Inf(1)
		return res
	}
	// Channel weights: mono and L/R are 1.0 under BS.1770.
	var powers []float64
	for start := int64(0); start+int64(block) <= res.Frames; start += int64(step) {
		var sum float64
		for c := 0; c < channels; c++ {
			var chSum float64
			for i := start; i < start+int64(block); i++ {
				v := pcm[i*int64(channels)+int64(c)]
				chSum += v * v
			}
			sum += chSum / float64(block)
		}
		powers = append(powers, sum)
	}
	// Absolute gate at -70 LUFS, then relative gate at -10 LU below the mean.
	absGate := math.Pow(10, (-70+0.691)/10)
	var gated []float64
	for _, p := range powers {
		if p >= absGate {
			gated = append(gated, p)
		}
	}
	if len(gated) == 0 {
		res.IntegratedLUFS = -math.Inf(1)
		return res
	}
	mean := meanOf(gated)
	relGate := mean * math.Pow(10, -1.0) // -10 LU
	var kept []float64
	for _, p := range gated {
		if p >= relGate {
			kept = append(kept, p)
		}
	}
	if len(kept) == 0 {
		kept = gated
	}
	res.IntegratedLUFS = loudnessOf(meanOf(kept))
	return res
}

func meanOf(v []float64) float64 {
	var s float64
	for _, x := range v {
		s += x
	}
	return s / float64(len(v))
}

func loudnessOf(power float64) float64 {
	if power <= 0 {
		return -math.Inf(1)
	}
	return -0.691 + 10*math.Log10(power)
}

func dbfs(peak float64) float64 {
	if peak <= 0 {
		return -math.Inf(1)
	}
	return 20 * math.Log10(peak)
}

// GainResult reports the outcome of loudness normalization.
type GainResult struct {
	Before       LoudnessResult
	After        LoudnessResult
	GainDB       float64
	Limited      bool
	ClippedAfter int
}

// ApplyGain applies gain and then a sample-peak limiter at ceilingDBFS.
// It never clips: peak samples above the ceiling are scaled down. Gain is
// capped at 0 dB so a quiet-but-correct master is not pushed into the limiter,
// and boosting is limited so the ceiling can be honoured with one gain factor.
func ApplyGain(pcm []float64, channels, rate int, targetLUFS, ceilingDBFS float64) ([]float64, GainResult) {
	before := Measure(pcm, channels, rate)
	res := GainResult{Before: before}
	out := append([]float64(nil), pcm...)
	gainDB := 0.0
	if !math.IsInf(before.IntegratedLUFS, 0) {
		gainDB = targetLUFS - before.IntegratedLUFS
	}
	ceiling := math.Pow(10, ceilingDBFS/20)
	peak := math.Pow(10, before.PeakDBFS/20)
	if peak > 0 {
		maxBoost := 20 * math.Log10(ceiling/peak)
		if gainDB > maxBoost {
			gainDB = maxBoost
			res.Limited = true
		}
	}
	res.GainDB = gainDB
	g := math.Pow(10, gainDB/20)
	for i := range out {
		out[i] *= g
	}
	// Sample-peak limiter; deterministic and order independent.
	for i := range out {
		if math.Abs(out[i]) > ceiling {
			out[i] = math.Copysign(ceiling, out[i])
			res.Limited = true
		}
	}
	res.After = Measure(out, channels, rate)
	res.ClippedAfter = res.After.Clipped
	return out, res
}
