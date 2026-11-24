package audio

import (
	"math"

	"mediapipe/internal/model"
)

// Window is one second of loudness statistics over a stereo (or mono folded)
// segment. RMS is computed in linear domain and reported as LUFS-like level
// using the K-weighting-free approximation LUFS = -0.691 + 10*log10(MSD).
type Window struct {
	Index       int     `json:"index"`
	RMS         float64 `json:"rms"`
	Peak        float64 `json:"peak"`
	LUFS        float64 `json:"loudness_lufs"`
	Silent      bool    `json:"silent"`
	GainApplied float64 `json:"gain_applied"`
	ClipCount   int     `json:"clip_count"`
}

// ControlConfig parameterizes loudness normalization.
type ControlConfig struct {
	TargetLUFS float64 // integrated loudness target, e.g. -23
	TruePeak   float64 // maximum permitted absolute sample, e.g. 1.0
	SampleRate int64   // samples per second per channel
}

// silenceFloor is the RMS below which a window is treated as silence. Such
// windows keep unity gain so background noise is never amplified.
const silenceFloor = 1e-6

// WindowStats measures one second (sampleRate frames per channel). PCM is
// interleaved stereo; the last partial second is still reported as its own
// window so no samples disappear.
func WindowStats(pcm []float64, sampleRate int64) []Window {
	perWindow := int(sampleRate) * 2
	if perWindow <= 0 {
		return nil
	}
	var windows []Window
	for start, idx := 0, 0; start < len(pcm); start, idx = start+perWindow, idx+1 {
		end := start + perWindow
		if end > len(pcm) {
			end = len(pcm)
		}
		var sumSq, peak float64
		n := 0
		for i := start; i < end; i++ {
			v := pcm[i]
			av := math.Abs(v)
			if av > peak {
				peak = av
			}
			sumSq += v * v
			n++
		}
		rms := math.Sqrt(sumSq / float64(n))
		w := Window{Index: idx, RMS: rms, Peak: peak}
		w.Silent = rms < silenceFloor
		if w.Silent {
			w.LUFS = math.Inf(-1)
		} else {
			w.LUFS = -0.691 + 10*math.Log10(rms*rms)
		}
		windows = append(windows, w)
	}
	return windows
}

// ApplyControl normalizes each window toward the target loudness and enforces
// the true-peak ceiling. Silent windows stay at unity gain; samples that hit
// the ceiling increment the clip count instead of wrapping.
func ApplyControl(pcm []float64, cfg ControlConfig) ([]float64, []Window, error) {
	if cfg.SampleRate <= 0 || cfg.TruePeak <= 0 {
		return nil, nil, model.NewError(model.ErrInvalidConfig, "", 0, 0,
			"loudness config requires positive sample rate and true peak")
	}
	windows := WindowStats(pcm, cfg.SampleRate)
	out := append([]float64(nil), pcm...)
	perWindow := int(cfg.SampleRate) * 2
	for _, w := range windows {
		gain := 1.0
		if !w.Silent {
			// Gain from loudness delta: 10^((target-current)/20). The
			// true-peak ceiling is enforced afterwards by the hard limiter,
			// which records every sample it has to pull back.
			gain = math.Pow(10, (cfg.TargetLUFS-w.LUFS)/20)
		}
		start := w.Index * perWindow
		end := start + perWindow
		if end > len(out) {
			end = len(out)
		}
		for i := start; i < end; i++ {
			v := out[i] * gain
			if math.Abs(v) > cfg.TruePeak {
				if v > 0 {
					v = cfg.TruePeak
				} else {
					v = -cfg.TruePeak
				}
				w.ClipCount++
			}
			out[i] = v
		}
		w.GainApplied = gain
		windows[w.Index] = w
	}
	return out, windows, nil
}

// IntegratedLUFS folds the per-window RMS values into one whole-track
// loudness figure, ignoring silent windows.
func IntegratedLUFS(windows []Window) float64 {
	var sumSq float64
	n := 0
	for _, w := range windows {
		if w.Silent {
			continue
		}
		sumSq += w.RMS * w.RMS
		n++
	}
	if n == 0 {
		return math.Inf(-1)
	}
	msd := sumSq / float64(n)
	return -0.691 + 10*math.Log10(msd)
}

// TotalClips sums clipping events across windows.
func TotalClips(windows []Window) int {
	total := 0
	for _, w := range windows {
		total += w.ClipCount
	}
	return total
}
