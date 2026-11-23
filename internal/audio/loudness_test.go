package audio

import (
	"math"
	"testing"
)

// sinePCM builds an interleaved sine at the given amplitude.
func sinePCM(rate, frames, channels int, freq, amp float64) []float64 {
	out := make([]float64, frames*channels)
	for i := 0; i < frames; i++ {
		v := amp * math.Sin(2*math.Pi*freq*float64(i)/float64(rate))
		for c := 0; c < channels; c++ {
			out[i*channels+c] = v
		}
	}
	return out
}

func TestMeasureReferenceTone(t *testing.T) {
	// Dual-mono sine at 0.7 keeps sample peak below full scale (0.7).
	pcm := sinePCM(48000, 48000, 2, 1000, 0.7)
	res := Measure(pcm, 2, 48000)
	if math.Abs(res.IntegratedLUFS-(loudnessOf(2*0.7*0.7/2))) > 0.3 {
		t.Fatalf("lufs = %f", res.IntegratedLUFS)
	}
	if math.Abs(res.PeakDBFS-20*math.Log10(0.7)) > 1e-6 {
		t.Fatalf("peak = %f", res.PeakDBFS)
	}
	if res.Clipped != 0 {
		t.Fatalf("clipped = %d", res.Clipped)
	}
}

func TestMeasureLoudnessTracksLevel(t *testing.T) {
	loud := Measure(sinePCM(48000, 24000, 1, 997, 0.5), 1, 48000)
	quiet := Measure(sinePCM(48000, 24000, 1, 997, 0.25), 1, 48000)
	diff := loud.IntegratedLUFS - quiet.IntegratedLUFS
	if math.Abs(diff-6.02) > 0.1 {
		t.Fatalf("halving amplitude changed loudness by %.3f LU, want 6.02", diff)
	}
	if quiet.PeakDBFS > -11.9 || quiet.PeakDBFS < -12.1 {
		t.Fatalf("peak = %f", quiet.PeakDBFS)
	}
}

func TestMeasureCountsClipping(t *testing.T) {
	pcm := []float64{1.0, -1.5, 0.2, 0.9}
	res := Measure(pcm, 2, 48000)
	if res.Clipped != 2 {
		t.Fatalf("clipped = %d", res.Clipped)
	}
}

func TestApplyGainReachesTargetWithoutClipping(t *testing.T) {
	pcm := sinePCM(48000, 48000, 2, 1000, 0.2)
	out, res := ApplyGain(pcm, 2, 48000, -23, -1)
	if math.Abs(res.After.IntegratedLUFS-(-23)) > 0.3 {
		t.Fatalf("after = %f (gain %.2f dB)", res.After.IntegratedLUFS, res.GainDB)
	}
	if res.After.Clipped != 0 {
		t.Fatalf("clipped after = %d", res.After.Clipped)
	}
	if res.After.PeakDBFS > -0.99 {
		t.Fatalf("peak = %f exceeds ceiling", res.After.PeakDBFS)
	}
	for _, v := range out {
		if math.Abs(v) > 1.0 {
			t.Fatalf("sample out of range: %f", v)
		}
	}
}

func TestApplyGainLimitsExtremeBoost(t *testing.T) {
	pcm := sinePCM(48000, 24000, 1, 440, 0.001)
	_, res := ApplyGain(pcm, 1, 48000, 0, -1)
	if !res.Limited {
		t.Fatal("expected limiter engagement")
	}
	if res.After.Clipped != 0 {
		t.Fatalf("clipped = %d", res.After.Clipped)
	}
}

func TestApplyGainHandlesSilence(t *testing.T) {
	_, res := ApplyGain(make([]float64, 1000), 1, 48000, -23, -1)
	if !math.IsInf(res.Before.IntegratedLUFS, -1) || res.GainDB != 0 {
		t.Fatalf("silence: %+v", res)
	}
}
