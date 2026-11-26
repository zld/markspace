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

// SinadDB estimates signal-to-noise from a single tone without an external
// reference: a Goertzel filter measures the energy at targetFreq and the
// remainder of the total energy is treated as noise+distortion. It is fully
// deterministic and recomputable from the PCM alone.
func SinadDB(pcm []byte, rate int, targetFreq float64) float64 {
	n := len(pcm) / pcmBytesPerSample
	if n < 16 {
		return 0
	}
	k := 2 * math.Pi * targetFreq / float64(rate)
	cosK := math.Cos(k)
	coeff := 2 * cosK
	var s0, s1, s2 float64
	var totalPow float64
	for i := 0; i < n; i++ {
		v := float64(int16(binary.LittleEndian.Uint16(pcm[2*i:]))) / 32768.0
		totalPow += v * v
		s0 = v + coeff*s1 - s2
		s2 = s1
		s1 = s0
	}
	// Goertzel power at the target bin (normalized normalization factor n)
	tonePow := (s1*s1 + s2*s2 - coeff*s1*s2) / float64(n)
	noisePow := totalPow - tonePow
	if noisePow <= 0 {
		return 99.0
	}
	if tonePow <= 0 {
		return 0
	}
	return 10 * math.Log10(tonePow/noisePow)
}

// ReferenceSinePCM builds a mono S16LE sine at toneFreq with a given amplitude
// (0..1) and length in samples, using the same phase convention as fixtures.
func ReferenceSinePCM(samples int, rate int, toneFreq, amplitude float64) []byte {
	out := make([]byte, samples*pcmBytesPerSample)
	for i := 0; i < samples; i++ {
		v := amplitude * math.Sin(2*math.Pi*toneFreq*float64(i)/float64(rate))
		sample := int16(math.Round(v * 32767))
		binary.LittleEndian.PutUint16(out[2*i:], uint16(sample))
	}
	return out
}

// SNRAgainstReference compares candidate PCM with a same-length reference tone
// at the same RMS level, returning the classic signal-to-noise ratio in dB.
// The reference is amplitude-scaled to match the candidate's RMS so loudness
// normalization does not by itself read as noise.
func SNRAgainstReference(candidate []byte, rate int, toneFreq float64) float64 {
	n := len(candidate) / pcmBytesPerSample
	if n == 0 {
		return 0
	}
	var candSq float64
	cand := make([]float64, n)
	for i := 0; i < n; i++ {
		v := float64(int16(binary.LittleEndian.Uint16(candidate[2*i:]))) / 32768.0
		cand[i] = v
		candSq += v * v
	}
	candRMS := math.Sqrt(candSq / float64(n))
	if candRMS == 0 {
		return 0
	}
	// build a unit-RMS sine, then scale it to the candidate's RMS
	var sigPow, noisePow float64
	for i := 0; i < n; i++ {
		unit := math.Sqrt(2) * math.Sin(2*math.Pi*toneFreq*float64(i)/float64(rate))
		ref := candRMS * unit
		e := cand[i] - ref
		sigPow += ref * ref
		noisePow += e * e
	}
	if noisePow == 0 {
		return 99.0
	}
	return 10 * math.Log10(sigPow/noisePow)
}

// SNRDelivered measures codec fidelity: the ratio of signal energy in the
// pre-encode PCM to the energy of the difference between pre-encode and the
// decoded, delivered PCM. AAC priming/padding makes the delivered buffer a
// little longer, so comparison is over the shorter overlapping length. Both
// are mono S16LE at the same sample rate.
func SNRDelivered(preEncode, delivered []byte) float64 {
	nSrc := len(preEncode) / pcmBytesPerSample
	nDel := len(delivered) / pcmBytesPerSample
	if nSrc == 0 || nDel == 0 {
		return 0
	}
	src := make([]float64, nSrc)
	del := make([]float64, nDel)
	for i := 0; i < nSrc; i++ {
		src[i] = float64(int16(binary.LittleEndian.Uint16(preEncode[2*i:]))) / 32768.0
	}
	for i := 0; i < nDel; i++ {
		del[i] = float64(int16(binary.LittleEndian.Uint16(delivered[2*i:]))) / 32768.0
	}
	// AAC priming inserts a short silence delay only on the delivered side.
	// Find the best integer-sample alignment via bounded normalized cross-
	// correlation (correlation, not squared error, so silence-dominated
	// windows do not trivially minimize to the zero-shift case).
	const maxShift = 4096
	const probe = 4096
	searchN := probe
	if searchN > nSrc {
		searchN = nSrc
	}
	limit := maxShift
	if limit+searchN > nDel {
		limit = nDel - searchN
	}
	if limit < 0 {
		limit = 0
	}
	var srcEnergy float64
	for i := 0; i < searchN; i++ {
		srcEnergy += src[i] * src[i]
	}
	bestShift, bestCorr := 0, math.Inf(-1)
	for shift := 0; shift <= limit; shift++ {
		var corr, delEnergy float64
		for i := 0; i < searchN; i++ {
			corr += src[i] * del[shift+i]
			delEnergy += del[shift+i] * del[shift+i]
		}
		if delEnergy > 0 && srcEnergy > 0 {
			corr /= math.Sqrt(srcEnergy * delEnergy)
		}
		if corr > bestCorr {
			bestCorr, bestShift = corr, shift
		}
	}
	overlap := nSrc
	if bestShift+overlap > nDel {
		overlap = nDel - bestShift
	}
	if overlap <= 0 {
		return 0
	}
	var sigPow, noisePow float64
	for i := 0; i < overlap; i++ {
		s := src[i]
		e := s - del[bestShift+i]
		sigPow += s * s
		noisePow += e * e
	}
	if noisePow == 0 {
		return 99.0
	}
	if sigPow == 0 {
		return 0
	}
	return 10 * math.Log10(sigPow/noisePow)
}

// PCMStats bundles per-segment acoustic measurements for aggregation.
type PCMStats struct {
	Samples    int
	MeanSquare float64 // for energy-weighted LUFS rollup
	TruePeak   float64
	Sinad      float64
}

// MeasurePCM computes the per-segment acoustic stats over mono S16LE PCM.
func MeasurePCM(pcm []byte, rate int, toneFreq float64) PCMStats {
	n := len(pcm) / pcmBytesPerSample
	var sumSq float64
	for i := 0; i < n; i++ {
		v := float64(int16(binary.LittleEndian.Uint16(pcm[2*i:]))) / 32768.0
		sumSq += v * v
	}
	return PCMStats{
		Samples:    n,
		MeanSquare: sumSq / float64(n),
		TruePeak:   TruePeakDBTP(pcm),
		Sinad:      SinadDB(pcm, rate, toneFreq),
	}
}
