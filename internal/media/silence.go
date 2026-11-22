package media

import (
	"math"

	"github.com/archive/mediakernel/internal/manifest"
	"github.com/archive/mediakernel/internal/rtprecover"
	"github.com/archive/mediakernel/internal/timeline"
)

// SilenceConfig controls silence classification on the recovered audio track.
type SilenceConfig struct {
	// Threshold is the RMS amplitude below which a window counts as silent.
	// Audio samples are interpreted as signed 16-bit PCM.
	ThresholdRMS float64
	// MinDuration is the shortest silent run that becomes a reported interval.
	MinDuration timeline.Time
	// Window is the analysis window size on the unified axis. Windows shorter
	// than this at a track boundary are reported with a partial status.
	Window timeline.Time
}

// DefaultSilenceConfig returns conservative archive defaults.
func DefaultSilenceConfig() SilenceConfig {
	return SilenceConfig{
		ThresholdRMS: 500, // ~ -54 dBFS for 16-bit PCM
		MinDuration:  200 * timeline.Millisecond,
		Window:       20 * timeline.Millisecond,
	}
}

// AudioFrame is one recovered audio frame with a timeline range and PCM bytes.
type AudioFrame struct {
	StartUS int64
	EndUS   int64
	PCM     []byte // signed 16-bit little-endian mono samples
}

// DetectSilence scans recovered audio frames on the unified timeline and emits
// silence intervals with explicit statuses. Unknown material is never called
// silent:
//   - partial-window: a window truncated by a track boundary;
//   - timestamp-regress: frame timestamps moved backwards;
//   - gap-covered: the window overlaps a confirmed loss gap.
func DetectSilence(frames []AudioFrame, gaps []manifest.LossGap, cfg SilenceConfig) ([]manifest.Silence, error) {
	if cfg.Window <= 0 || cfg.MinDuration <= 0 {
		return nil, timeline.NewError(timeline.KindInvalidInput, "silence window and min duration must be positive")
	}
	var out []manifest.Silence
	var runStart, runEnd int64
	runActive := false
	var regressReported bool

	flush := func(endUS int64) {
		if runActive && endUS-runStart >= int64(cfg.MinDuration) {
			out = append(out, manifest.Silence{StartUS: runStart, EndUS: endUS, Status: manifest.StatusSilent})
		}
		runActive = false
	}

	for _, f := range frames {
		if f.EndUS <= f.StartUS {
			return nil, timeline.NewError(timeline.KindInvalidInput, "frame with non-positive duration at %d", f.StartUS)
		}
		// Timestamp regression is a hard data-quality fault; report once and
		// stop classifying rather than emit a misleading silence.
		if f.StartUS < runEnd {
			if !regressReported {
				out = append(out, manifest.Silence{
					StartUS: f.StartUS, EndUS: f.EndUS, Status: manifest.StatusTimestampRegress,
				})
				regressReported = true
			}
			flush(runEnd)
			continue
		}
		// Gap coverage: any overlap with a confirmed gap makes the frame
		// unknown, not silent.
		if rtprecover.Covers(gaps, f.StartUS, f.EndUS) {
			flush(runEnd)
			out = append(out, manifest.Silence{
				StartUS: f.StartUS, EndUS: f.EndUS, Status: manifest.StatusGapCovered,
			})
			runEnd = f.EndUS
			continue
		}
		// Boundary windows shorter than the configured window stay explicit.
		windowDur := f.EndUS - f.StartUS
		if windowDur < int64(cfg.Window) {
			flush(runEnd)
			out = append(out, manifest.Silence{
				StartUS: f.StartUS, EndUS: f.EndUS, Status: manifest.StatusPartialWindow,
			})
			runEnd = f.EndUS
			continue
		}
		silent := rms16(f.PCM) < cfg.ThresholdRMS
		if silent {
			if !runActive {
				runActive = true
				runStart = f.StartUS
			}
			runEnd = f.EndUS
		} else {
			flush(runEnd)
		}
		runEnd = f.EndUS
	}
	flush(runEnd)
	return out, nil
}

// rms16 computes the root-mean-square of signed 16-bit little-endian samples.
// An odd trailing byte is ignored rather than producing a phantom sample.
func rms16(pcm []byte) float64 {
	n := len(pcm) / 2
	if n == 0 {
		return 0
	}
	sumSq := 0.0
	for i := 0; i < n; i++ {
		lo := pcm[2*i]
		hi := pcm[2*i+1]
		v := float64(int16(uint16(lo) | uint16(hi)<<8))
		sumSq += v * v
	}
	return math.Sqrt(sumSq / float64(n))
}
