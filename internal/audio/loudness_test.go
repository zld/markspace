package audio

import (
	"math"
	"testing"
)

func TestMeasureSilentAndEmptyAreFinite(t *testing.T) {
	empty, _ := NewBuffer(Stereo, 0)
	for name, b := range map[string]*Buffer{"empty": empty, "nil": nil} {
		res := Measure(b)
		if math.IsNaN(res.IntegratedLUFS) || math.IsInf(res.IntegratedLUFS, 0) {
			t.Fatalf("%s: non-finite loudness %v", name, res.IntegratedLUFS)
		}
		if res.IntegratedLUFS != SilenceLUFS {
			t.Fatalf("%s: loudness = %v, want silence floor", name, res.IntegratedLUFS)
		}
		if math.IsNaN(res.SamplePeakDBFS) || math.IsInf(res.SamplePeakDBFS, 0) {
			t.Fatalf("%s: non-finite peak %v", name, res.SamplePeakDBFS)
		}
	}
	silent, _ := NewBuffer(Stereo, 4800)
	res := MeasureAt(silent, 48000)
	if res.IntegratedLUFS != SilenceLUFS || res.SilentFraction != 1 {
		t.Fatalf("silent buffer: %+v", res)
	}
}

func TestMeasureToneHasExpectedOrderOfMagnitude(t *testing.T) {
	// A 1 kHz sine at amplitude 0.5 in both channels. With the kernel's
	// weighting this must land in a sane band, not at an absurd level.
	b := tone(Stereo, 48000, 48000, 1000, 0.5)
	res := MeasureAt(b, 48000)
	if res.Gated == 0 {
		t.Fatal("no gated blocks for a steady tone")
	}
	if res.IntegratedLUFS > -6 || res.IntegratedLUFS < -30 {
		t.Fatalf("loudness %0.2f outside plausible band", res.IntegratedLUFS)
	}
	// Peak must be the actual amplitude in dBFS.
	wantPeak := 20 * math.Log10(0.5)
	if math.Abs(res.SamplePeakDBFS-wantPeak) > 0.01 {
		t.Fatalf("peak %0.3f, want %0.3f", res.SamplePeakDBFS, wantPeak)
	}
}

func TestApplyGainHitsTargetWithinTolerance(t *testing.T) {
	b := tone(Stereo, 48000, 48000, 1000, 0.2)
	target := -16.0
	out, gr, err := ApplyGain(b, GainRequest{TargetLUFS: target, PeakDBFS: -1})
	if err != nil {
		t.Fatal(err)
	}
	if gr.Silent || gr.Empty {
		t.Fatalf("tone treated as silent: %+v", gr)
	}
	if math.Abs(gr.Output.IntegratedLUFS-target) > 1.5 {
		t.Fatalf("output loudness %0.3f misses target %0.1f by more than 1.5 LU (gain %+v)",
			gr.Output.IntegratedLUFS, target, gr)
	}
	if gr.Output.TruePeakDBFS > -1+1e-9 {
		t.Fatalf("peak ceiling exceeded: %0.2f", gr.Output.TruePeakDBFS)
	}
	if err := out.Validate(); err != nil {
		t.Fatal(err)
	}
}

func TestApplyGainRespectsPeakCeiling(t *testing.T) {
	// A very quiet signal requesting a very loud target must be limited by
	// the ceiling, not by loudness.
	b := tone(Stereo, 48000, 48000, 1000, 0.01)
	out, gr, err := ApplyGain(b, GainRequest{TargetLUFS: -5, PeakDBFS: -3})
	if err != nil {
		t.Fatal(err)
	}
	if !gr.GainLimited {
		t.Fatalf("expected peak limiting: %+v", gr)
	}
	if gr.Output.TruePeakDBFS > -3+0.01 {
		t.Fatalf("ceiling exceeded: %0.3f", gr.Output.TruePeakDBFS)
	}
	if CountClipped(out, 1.0) != 0 {
		t.Fatal("limited gain produced clipping")
	}
}

func TestApplyGainOnSilentAndEmptyIsNoOpAndFinite(t *testing.T) {
	silent, _ := NewBuffer(Stereo, 4800)
	out, gr, err := ApplyGain(silent, GainRequest{TargetLUFS: -16, PeakDBFS: -1})
	if err != nil {
		t.Fatal(err)
	}
	if !gr.Silent || gr.GainDB != 0 {
		t.Fatalf("silent input: %+v", gr)
	}
	if math.IsNaN(gr.Output.IntegratedLUFS) {
		t.Fatal("silent output loudness is NaN")
	}
	if len(out.Data) != len(silent.Data) {
		t.Fatal("silent processing changed buffer length")
	}

	empty, _ := NewBuffer(Stereo, 0)
	_, gr, err = ApplyGain(empty, GainRequest{TargetLUFS: -16, PeakDBFS: -1})
	if err != nil {
		t.Fatal(err)
	}
	if !gr.Empty || gr.GainDB != 0 {
		t.Fatalf("empty input: %+v", gr)
	}
}

func TestApplyGainRejectsOutOfRangeTargets(t *testing.T) {
	b := tone(Stereo, 100, 48000, 440, 0.2)
	if _, _, err := ApplyGain(b, GainRequest{TargetLUFS: 3, PeakDBFS: -1}); err == nil {
		t.Fatal("positive target accepted")
	}
	if _, _, err := ApplyGain(b, GainRequest{TargetLUFS: -16, PeakDBFS: 2}); err == nil {
		t.Fatal("positive peak ceiling accepted")
	}
}

func TestClippedInputIsCountedNotHidden(t *testing.T) {
	b := tone(Stereo, 480, 48000, 440, 1.0)
	// Force true full-scale samples so the counter has something to find.
	for i := range b.Data {
		b.Data[i] = 1.0
	}
	gr := GainResult{}
	out, res, err := ApplyGain(b, GainRequest{TargetLUFS: -16, PeakDBFS: -1})
	if err != nil {
		t.Fatal(err)
	}
	gr = res
	if gr.ClippedInput == 0 {
		t.Fatal("clipped input not counted")
	}
	// The ceiling must pull the level down so the output no longer clips.
	if CountClipped(out, 1.0) != 0 {
		t.Fatal("output still clips after gain control")
	}
}

func TestStatsAccumulateAndValidate(t *testing.T) {
	s := Stats{}
	s.Observe([]float64{0, 0})
	s.Observe([]float64{0.5, -1.0})
	s.Observe([]float64{2.0, 0.25})
	if s.OutputFrames != 3 {
		t.Fatalf("frames = %d", s.OutputFrames)
	}
	// -1.0 and 2.0 both reach full scale, so both count as clipped.
	if s.ClippedSamples != 2 {
		t.Fatalf("clipped = %d, want 2", s.ClippedSamples)
	}
	if s.SilentSamples != 2 {
		t.Fatalf("silent samples = %d, want 2", s.SilentSamples)
	}
	if s.PeakDBFS() <= 0 {
		t.Fatalf("peak should exceed 0 dBFS for a 2.0 sample: %f", s.PeakDBFS())
	}
	var other Stats
	other.Add(s)
	if other.OutputFrames != s.OutputFrames || other.SumSquares != s.SumSquares {
		t.Fatal("Add lost data")
	}
	if err := s.Validate(); err != nil {
		t.Fatalf("valid stats rejected: %v", err)
	}
	bad := Stats{SilentSamples: 400, OutputFrames: 1}
	if err := bad.Validate(); err == nil {
		t.Fatal("impossible stats accepted")
	}
}
