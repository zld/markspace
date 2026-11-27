package audio

import (
	"math"
	"testing"
)

func TestMeasureLevels(t *testing.T) {
	// Full-scale square wave at +/-1 has RMS 1 -> 0 dBFS and peak 0 dBTP.
	in := Buffer{Samples: []float64{1, -1, 1, -1}, Frames: 4, Ch: 1}
	lufs, peak := Measure(in)
	if math.Abs(lufs) > 1e-9 || math.Abs(peak) > 1e-9 {
		t.Fatalf("lufs=%v peak=%v, want 0/0", lufs, peak)
	}
	// Half-scale 0.5 -> 20*log10(0.5) ~= -6.02 dB.
	half := Buffer{Samples: []float64{0.5, 0.5}, Frames: 2, Ch: 1}
	l2, p2 := Measure(half)
	want := 20 * math.Log10(0.5)
	if math.Abs(l2-want) > 1e-9 || math.Abs(p2-want) > 1e-9 {
		t.Fatalf("half levels = %v/%v, want %v", l2, p2, want)
	}
}

func TestNormalizeLoudnessHitsTarget(t *testing.T) {
	// A -6 dBFS signal normalized to -16 LUFS: gain -10 dB, peak lands at
	// -16.02 dBTP which is under the -1.0 ceiling, so both gates pass.
	in := Buffer{Samples: []float64{0.5, -0.5, 0.5, -0.5}, Frames: 4, Ch: 1}
	out, res, err := NormalizeLoudness(in, LoudnessTarget{IntegratedLUFS: -16, TruePeakDBTP: -1})
	if err != nil {
		t.Fatal(err)
	}
	if math.Abs(res.GainDB-(-9.9794)) > 1e-6 {
		t.Fatalf("gain = %v, want ~-9.98 dB", res.GainDB)
	}
	if !res.LoudnessMet || !res.TruePeakMet {
		t.Fatalf("gates not met: %+v", res)
	}
	if math.Abs(res.LoudnessErrorLU) > 0.5 {
		t.Fatalf("loudness error = %v, want <= 0.5 LU", res.LoudnessErrorLU)
	}
	wantSample := 0.5 * math.Pow(10, res.GainDB/20)
	if math.Abs(out.Samples[0]-wantSample) > 1e-9 {
		t.Fatal("output sample mismatch")
	}
}

func TestNormalizeLoudnessPeakGateWins(t *testing.T) {
	// Already at -16 LUFS but with a peak that would breach -1 dBTP after
	// loudness gain: here the loudness target needs positive gain and the
	// peak ceiling caps it, so loudness ends below target but peak is safe.
	samples := make([]float64, 100)
	for i := range samples {
		// Mostly quiet with one loud transient.
		samples[i] = 0.01
	}
	samples[0] = 1.0
	in := Buffer{Samples: samples, Frames: 100, Ch: 1}
	_, res, err := NormalizeLoudness(in, LoudnessTarget{IntegratedLUFS: -10, TruePeakDBTP: -1})
	if err != nil {
		t.Fatal(err)
	}
	if !res.TruePeakMet {
		t.Fatalf("true peak must be protected: %+v", res)
	}
	if res.OutputTruePeak > -1.0+1e-9 {
		t.Fatalf("output peak %v exceeds -1 dBTP", res.OutputTruePeak)
	}
	// Peak gate must have limited the gain below the loudness-only value.
	loudnessOnly := -10.0 - res.InputLUFS
	if res.GainDB > loudnessOnly+1e-9 {
		t.Fatalf("gain %v exceeded loudness-only %v", res.GainDB, loudnessOnly)
	}
}

func TestNormalizeLoudnessSilence(t *testing.T) {
	in := NewBuffer(8, 2)
	_, res, err := NormalizeLoudness(in, LoudnessTarget{IntegratedLUFS: -16, TruePeakDBTP: -1})
	if err != nil {
		t.Fatal(err)
	}
	if res.LoudnessMet {
		t.Fatal("silence cannot meet a finite loudness target")
	}
	if !res.TruePeakMet {
		t.Fatal("silence trivially meets the true-peak ceiling")
	}
}
