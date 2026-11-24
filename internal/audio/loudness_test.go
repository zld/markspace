package audio

import (
	"math"
	"testing"

	"mediapipe/internal/model"
)

func TestWindowStatsPerSecond(t *testing.T) {
	rate := 100 // small synthetic rate: 100 frames/s, stereo
	pcm := make([]float64, rate*2*3)
	for i := range pcm {
		pcm[i] = 0.5
	}
	// Zero out the middle second -> one silent window.
	for i := rate * 2; i < rate*4; i++ {
		pcm[i] = 0
	}
	ws := WindowStats(pcm, int64(rate))
	if len(ws) != 3 {
		t.Fatalf("windows = %d, want 3", len(ws))
	}
	if ws[0].Silent || !ws[1].Silent || ws[2].Silent {
		t.Fatalf("silence flags wrong: %+v %+v %+v", ws[0], ws[1], ws[2])
	}
	if math.Abs(ws[0].RMS-0.5) > 1e-12 || ws[0].Peak != 0.5 {
		t.Fatalf("stats wrong: %+v", ws[0])
	}
	if !math.IsInf(ws[1].LUFS, -1) {
		t.Fatalf("silent window LUFS = %v, want -Inf", ws[1].LUFS)
	}
}

func TestSilenceNotAmplifiedAndClipping(t *testing.T) {
	rate := 50
	// First second silent, second second a quiet tone that loudness gain
	// toward 0 LUFS pushes past the true-peak ceiling.
	pcm := make([]float64, rate*2*2)
	for i := rate * 2; i < len(pcm); i++ {
		pcm[i] = 0.05
	}
	cfg := ControlConfig{TargetLUFS: 0, TruePeak: 1.0, SampleRate: int64(rate)}
	out, ws, err := ApplyControl(pcm, cfg)
	if err != nil {
		t.Fatal(err)
	}
	// Silent window: unity gain, output remains exactly zero.
	for i := 0; i < rate*2; i++ {
		if out[i] != 0 || ws[0].GainApplied != 1 {
			t.Fatal("silent window was modified or amplified")
		}
	}
	// Boosted window: every scaled sample crosses the ceiling and is counted.
	if ws[1].ClipCount != rate*2 {
		t.Fatalf("clip count = %d, want %d", ws[1].ClipCount, rate*2)
	}
	for i := rate * 2; i < len(out); i++ {
		if math.Abs(out[i]) > 1.0 {
			t.Fatal("true-peak ceiling violated")
		}
	}
}

func TestGainMovesTowardTarget(t *testing.T) {
	rate := 100
	pcm := make([]float64, rate*2)
	for i := range pcm {
		pcm[i] = 0.05 // quiet material, well below -16 LUFS
	}
	cfg := ControlConfig{TargetLUFS: -16, TruePeak: 1.0, SampleRate: int64(rate)}
	_, ws, err := ApplyControl(pcm, cfg)
	if err != nil {
		t.Fatal(err)
	}
	if ws[0].GainApplied <= 1 {
		t.Fatalf("quiet material not boosted: gain=%v", ws[0].GainApplied)
	}
	if lufs := IntegratedLUFS(ws); math.IsInf(lufs, -1) {
		t.Fatal("non-silent track must have finite integrated LUFS")
	}
}

func TestLoudnessRejectsBadConfig(t *testing.T) {
	_, _, err := ApplyControl([]float64{1}, ControlConfig{})
	if model.AsMediaError(err) == nil {
		t.Fatal("zero-value config must be rejected")
	}
}

func TestDeterministicRepeatedControl(t *testing.T) {
	rate := 80
	pcm := make([]float64, rate*2)
	for i := range pcm {
		pcm[i] = math.Sin(float64(i) * 0.1)
	}
	cfg := ControlConfig{TargetLUFS: -23, TruePeak: 1.0, SampleRate: int64(rate)}
	a, wa, _ := ApplyControl(pcm, cfg)
	b, wb, _ := ApplyControl(pcm, cfg)
	for i := range a {
		if a[i] != b[i] {
			t.Fatal("loudness control not deterministic")
		}
	}
	if TotalClips(wa) != TotalClips(wb) {
		t.Fatal("clip counts differ across runs")
	}
}
