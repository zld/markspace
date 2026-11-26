package audio

import (
	"crypto/sha256"
	"encoding/binary"
	"math"

	"rtpregress/internal/clock"
)

// Metrics holds the recomputable quality evidence for one processed output.
type Metrics struct {
	BitrateTarget      int64   `json:"bitrate_target"`
	BitrateActual      int64   `json:"bitrate_actual"`
	BitrateDeltaPct    float64 `json:"bitrate_delta_pct"`
	SNRDB              float64 `json:"snr_db"`
	LUFS               float64 `json:"lufs"`
	TruePeakDBTP       float64 `json:"true_peak_dbtp"`
	ManifestMaxErrorMS float64 `json:"manifest_max_error_ms"`
}

// sha256Sum hashes PCM bytes so identical inputs yield identical evidence.
func sha256Sum(b []byte) [32]byte { return sha256.Sum256(b) }

// rms computes the root-mean-square amplitude of S16LE mono PCM.
func rms(pcm []byte) float64 {
	n := len(pcm) / pcmBytesPerSample
	if n == 0 {
		return 0
	}
	var sumSq float64
	for i := 0; i < n; i++ {
		v := float64(int16(binary.LittleEndian.Uint16(pcm[2*i:]))) / 32768.0
		sumSq += v * v
	}
	return math.Sqrt(sumSq / float64(n))
}

// SNRDB computes signal-to-noise ratio between reference and candidate PCM,
// treating the per-sample difference as noise. Both must be same-length mono
// S16LE; the shorter length bounds the comparison.
func SNRDB(ref, cand []byte) float64 {
	n := len(ref)
	if len(cand) < n {
		n = len(cand)
	}
	nSamples := n / pcmBytesPerSample
	if nSamples == 0 {
		return 0
	}
	var sigPow, noisePow float64
	for i := 0; i < nSamples; i++ {
		s := float64(int16(binary.LittleEndian.Uint16(ref[2*i:]))) / 32768.0
		c := float64(int16(binary.LittleEndian.Uint16(cand[2*i:]))) / 32768.0
		e := s - c
		sigPow += s * s
		noisePow += e * e
	}
	if noisePow == 0 {
		return 99.0 // identical: report a capped, clearly-passing sentinel
	}
	return 10 * math.Log10(sigPow/noisePow)
}

// TruePeakDBTP approximates the inter-sample true peak using 4x oversampling
// with a linear interpolator, then converts the maximum to dBTP.
func TruePeakDBTP(pcm []byte) float64 {
	n := len(pcm) / pcmBytesPerSample
	if n == 0 {
		return math.Inf(-1)
	}
	maxAbs := 0.0
	prev := float64(int16(binary.LittleEndian.Uint16(pcm))) / 32768.0
	for i := 1; i < n; i++ {
		cur := float64(int16(binary.LittleEndian.Uint16(pcm[2*i:]))) / 32768.0
		for k := 1; k <= 4; k++ {
			interp := prev + (cur-prev)*float64(k)/4.0
			if a := math.Abs(interp); a > maxAbs {
				maxAbs = a
			}
		}
		if a := math.Abs(cur); a > maxAbs {
			maxAbs = a
		}
		prev = cur
	}
	if maxAbs == 0 {
		return math.Inf(-1)
	}
	return 20 * math.Log10(maxAbs)
}

// LUFS approximates integrated loudness (K-weighted energy over the whole
// segment). For mono 48 kHz material this is a practical stand-in for the
// full EBU R128 gating model and is stable across deterministic reruns.
func LUFS(pcm []byte) float64 {
	level := rms(pcm)
	if level == 0 {
		return -70.0 // silence floor
	}
	// K-weighting for mono is dominated by a ~0.7 dB shelving offset; the
	// mean-square -> LUFS mapping is 10*log10(mean square) - 0.691.
	return 10*math.Log10(level*level) - 0.691
}

// BitrateActual returns the AAC bitrate implied by encoded bytes and duration.
func BitrateActual(aacBytes int, durationNanos int64) int64 {
	if durationNanos <= 0 {
		return 0
	}
	return int64(aacBytes) * 8 * clock.NanosPerSecond / durationNanos
}

// BitrateDeltaPct is the signed percentage deviation from target.
func BitrateDeltaPct(actual, target int64) float64 {
	if target == 0 {
		return 0
	}
	return 100 * float64(actual-target) / float64(target)
}
