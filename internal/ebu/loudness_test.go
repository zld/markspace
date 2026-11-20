package ebu

import (
	"math"
	"math/cmplx"
	"testing"
)

// ampFor solves for the sine amplitude that, after K-weighting, yields the
// requested integrated loudness when the same signal is played on every
// channel. weightsSum is the sum of BS.1770 channel weights.
func ampFor(t *testing.T, sampleRate int, freq, targetLUFS, weightsSum float64) float64 {
	t.Helper()
	kw, err := KWeightingFor(sampleRate)
	if err != nil {
		t.Fatal(err)
	}
	fs := float64(sampleRate)
	h := cmplx.Abs(response(kw.HighPass, fs, freq) * response(kw.Shelf, fs, freq))
	// target = -0.691 + 10*log10(weightsSum * A^2/2 * h^2)
	energy := math.Pow(10, (targetLUFS+0.691)/10) / weightsSum / (h * h)
	return math.Sqrt(2 * energy)
}

func sineFrames(sampleRate, channels int, seconds, amplitude, freq float64) []float64 {
	n := int(math.Round(seconds * float64(sampleRate)))
	out := make([]float64, n*channels)
	for i := 0; i < n; i++ {
		v := amplitude * math.Sin(2*math.Pi*freq*float64(i)/float64(sampleRate))
		for c := 0; c < channels; c++ {
			out[i*channels+c] = v
		}
	}
	return out
}

func measure(t *testing.T, sampleRate int, labels []string, frames []float64) Result {
	t.Helper()
	layout, err := NewLayout(labels)
	if err != nil {
		t.Fatalf("layout: %v", err)
	}
	m, err := NewMeter(sampleRate, layout)
	if err != nil {
		t.Fatalf("meter: %v", err)
	}
	if err := m.ProcessInterleaved(frames); err != nil {
		t.Fatalf("process: %v", err)
	}
	return m.Finish()
}

func TestStereoSineIsMinus23LUFS(t *testing.T) {
	amp := ampFor(t, 48000, 1000, -23, 2)
	frames := sineFrames(48000, 2, 6, amp, 1000)
	res := measure(t, 48000, []string{"FL", "FR"}, frames)
	if !res.Measured {
		t.Fatal("expected a measurement")
	}
	if math.Abs(res.IntegratedLUFS-(-23)) > 0.15 {
		t.Errorf("integrated = %.3f LUFS, want -23", res.IntegratedLUFS)
	}
	if math.Abs(res.MaxShortTermLUFS-(-23)) > 0.15 {
		t.Errorf("max short-term = %.3f LUFS, want -23", res.MaxShortTermLUFS)
	}
}

func TestMonoSineIsThreeDBLower(t *testing.T) {
	amp := ampFor(t, 48000, 1000, -23, 1)
	mono := measure(t, 48000, []string{"FC"}, sineFrames(48000, 1, 4, amp, 1000))
	if math.Abs(mono.IntegratedLUFS-(-23)) > 0.15 {
		t.Errorf("mono integrated = %.3f, want -23", mono.IntegratedLUFS)
	}
	stereo := measure(t, 48000, []string{"FL", "FR"}, sineFrames(48000, 2, 4, amp, 1000))
	if math.Abs((stereo.IntegratedLUFS-mono.IntegratedLUFS)-3.01) > 0.05 {
		t.Errorf("stereo-mono difference = %.3f, want 3.01", stereo.IntegratedLUFS-mono.IntegratedLUFS)
	}
}

func TestSurroundWeighting(t *testing.T) {
	// Same signal on every channel; the two surrounds are weighted 1.41, so
	// total weighted energy is 3*1.0 + 2*1.41 = 5.82 times one channel.
	const weightsSum = 3*1.0 + 2*1.41
	amp := ampFor(t, 48000, 1000, -23, weightsSum)
	frames := sineFrames(48000, 5, 4, amp, 1000)
	res := measure(t, 48000, []string{"FL", "FR", "FC", "SL", "SR"}, frames)
	if math.Abs(res.IntegratedLUFS-(-23)) > 0.15 {
		t.Errorf("5.0 integrated = %.3f, want -23", res.IntegratedLUFS)
	}
	// A 7.1 layout is FL FR FC LFE BL BR SL SR, i.e. three channels at 1.0
	// and four at 1.41, so the applied weights must track the layout.
	const w71 = 3*1.0 + 4*1.41
	amp71 := ampFor(t, 48000, 1000, -23, w71)
	res71 := measure(t, 48000, []string{"FL", "FR", "FC", "LFE", "BL", "BR", "SL", "SR"},
		sineFrames(48000, 8, 4, amp71, 1000))
	if math.Abs(res71.IntegratedLUFS-(-23)) > 0.15 {
		t.Errorf("7.1 integrated = %.3f, want -23", res71.IntegratedLUFS)
	}
}

func TestLFEIsNotMeasured(t *testing.T) {
	// Silence in every weighted channel, a loud LFE: no measurement.
	layout, _ := NewLayout([]string{"FL", "FR", "LFE"})
	m, err := NewMeter(48000, layout)
	if err != nil {
		t.Fatal(err)
	}
	frames := sineFrames(48000, 3, 2, 0.9, 60)
	for i := range frames {
		if i%3 != 2 {
			frames[i] = 0
		}
	}
	if err := m.ProcessInterleaved(frames); err != nil {
		t.Fatal(err)
	}
	res := m.Finish()
	if res.Measured {
		t.Fatalf("LFE-only material measured %.3f LUFS", res.IntegratedLUFS)
	}
	if !math.IsInf(res.IntegratedLUFS, -1) {
		t.Errorf("integrated = %v, want -Inf", res.IntegratedLUFS)
	}
}

func TestSilenceIsNoMeasurement(t *testing.T) {
	frames := make([]float64, 48000*2*2)
	res := measure(t, 48000, []string{"FL", "FR"}, frames)
	if res.Measured {
		t.Fatal("silence produced a measurement")
	}
	if !math.IsInf(res.IntegratedLUFS, -1) || math.IsNaN(res.IntegratedLUFS) {
		t.Errorf("integrated = %v", res.IntegratedLUFS)
	}
}

func TestShortMaterialStillMeasured(t *testing.T) {
	amp := ampFor(t, 48000, 1000, -23, 2)
	frames := sineFrames(48000, 2, 0.2, amp, 1000)
	res := measure(t, 48000, []string{"FL", "FR"}, frames)
	if !res.Measured || !res.PartialShort || !res.PartialIntegrate {
		t.Fatalf("short material result: %+v", res)
	}
	// Energy over the real 0.2 s must not be diluted by fabricated silence.
	if math.Abs(res.IntegratedLUFS-(-23)) > 0.2 {
		t.Errorf("integrated = %.3f, want about -23", res.IntegratedLUFS)
	}
}

func TestChunkBoundariesDoNotResetFilter(t *testing.T) {
	amp := ampFor(t, 48000, 1000, -23, 2)
	frames := sineFrames(48000, 2, 4, amp, 1000)
	whole := measure(t, 48000, []string{"FL", "FR"}, frames)

	layout, _ := NewLayout([]string{"FL", "FR"})
	m, _ := NewMeter(48000, layout)
	// Deliberately odd chunk size that never aligns with 100 ms segments.
	for start := 0; start < len(frames); start += 777 * 2 {
		end := start + 777*2
		if end > len(frames) {
			end = len(frames)
		}
		if err := m.ProcessInterleaved(frames[start:end]); err != nil {
			t.Fatal(err)
		}
	}
	res := m.Finish()
	if math.Abs(res.IntegratedLUFS-whole.IntegratedLUFS) > 1e-9 {
		t.Errorf("chunked %.9f vs whole %.9f", res.IntegratedLUFS, whole.IntegratedLUFS)
	}
}

func TestNonFiniteSampleRejected(t *testing.T) {
	layout, _ := NewLayout([]string{"FC"})
	m, _ := NewMeter(48000, layout)
	if err := m.ProcessInterleaved([]float64{0, math.NaN()}); err == nil {
		t.Fatal("NaN sample accepted")
	}
	if err := m.ProcessInterleaved([]float64{0, math.Inf(1)}); err == nil {
		t.Fatal("Inf sample accepted")
	}
}

func TestRelativeGateRemovesQuietPassages(t *testing.T) {
	amp := ampFor(t, 48000, 1000, -23, 2)
	loud := sineFrames(48000, 2, 3, amp, 1000)
	soft := sineFrames(48000, 2, 6, amp*0.01, 1000)
	frames := append(append([]float64{}, loud...), soft...)
	res := measure(t, 48000, []string{"FL", "FR"}, frames)
	if !res.Measured {
		t.Fatal("expected measurement")
	}
	// The quiet 6 s is 40 dB down; relative gating must keep the result near
	// the loud section rather than averaging both together.
	if math.Abs(res.IntegratedLUFS-(-23)) > 0.5 {
		t.Errorf("gated integrated = %.3f, want about -23", res.IntegratedLUFS)
	}
}
