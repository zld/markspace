package audio

import (
	"math"
	"testing"
)

func TestApplyLoudnessGainAndLimit(t *testing.T) {
	// Sparse full-scale spikes: low RMS invites uplift, and the uplifted
	// peaks must hit the true-peak ceiling.
	cfg := LoudnessConfig{TargetLUFS: -8, MaxGain: 20, TruePeakDB: -2}
	in := Frame{SampleRate: 48000, Channels: 1, Samples: make([]int16, 100)}
	for i := range in.Samples {
		if i%10 == 0 {
			in.Samples[i] = 32767
		}
	}
	out, st := ApplyLoudness(in, cfg)
	if st.GainDB <= 0 {
		t.Fatalf("low-RMS input must be uplifted, gain=%f", st.GainDB)
	}
	if st.Limited == 0 {
		t.Fatal("uplifted full-scale peaks must hit the true-peak ceiling")
	}
	ceiling := int16(math.Round(32767 * math.Pow(10, -2/20.0)))
	for _, s := range out.Samples {
		if s > ceiling || s < -ceiling {
			t.Fatalf("sample %d breaches ceiling %d", s, ceiling)
		}
	}
}

func TestApplyLoudnessUpliftCapped(t *testing.T) {
	// Very quiet input: gain is uplifted but never beyond MaxGain.
	cfg := LoudnessConfig{TargetLUFS: -16, MaxGain: 6, TruePeakDB: -1}
	in := Frame{SampleRate: 48000, Channels: 1, Samples: make([]int16, 50)}
	for i := range in.Samples {
		in.Samples[i] = 4
	}
	_, st := ApplyLoudness(in, cfg)
	if st.GainDB > 6.0+0.001 {
		t.Fatalf("gain %f exceeds cap 6 dB", st.GainDB)
	}
	if st.InputLUFS >= st.OutputLUFS {
		t.Fatalf("uplift expected: in=%f out=%f", st.InputLUFS, st.OutputLUFS)
	}
}

func TestProcessPipelineDeterministic(t *testing.T) {
	in := Frame{SampleRate: 44100, Channels: 6, Samples: buildPCM(600, 6)}
	cfg := PipelineConfig{TargetRate: 48000, Loudness: LoudnessConfig{
		TargetLUFS: -16, MaxGain: 12, TruePeakDB: -1,
	}}
	out1, st1, err := Process(in, cfg)
	if err != nil {
		t.Fatal(err)
	}
	out2, st2, err := Process(in, cfg)
	if err != nil {
		t.Fatal(err)
	}
	if len(out1.Samples) != len(out2.Samples) || st1.Checksum != st2.Checksum {
		t.Fatal("repeated runs diverged")
	}
	for i := range out1.Samples {
		if out1.Samples[i] != out2.Samples[i] {
			t.Fatalf("sample divergence at %d", i)
		}
	}
	if st1.OutputRate != 48000 || st1.OutputChannels != 2 {
		t.Fatalf("pipeline output shape wrong: %+v", st1)
	}
	if st1.Periods <= 0 || st1.RMS <= 0 || st1.PeakAbs <= 0 {
		t.Fatalf("stats must be populated and positive: %+v", st1)
	}
	// All output samples stay within s16 range by construction.
	for _, s := range out1.Samples {
		if s < -32768 || s > 32767 {
			t.Fatalf("out of s16 range: %d", s)
		}
	}
}

func TestProcessRejectsEmptyAndUnsupported(t *testing.T) {
	_, _, err := Process(Frame{SampleRate: 48000, Channels: 2, Samples: nil},
		PipelineConfig{TargetRate: 48000})
	if err == nil {
		t.Fatal("empty frame must error")
	}
	_, _, err = Process(Frame{SampleRate: 48000, Channels: 5, Samples: []int16{1, 2, 3, 4, 5}},
		PipelineConfig{TargetRate: 48000})
	if err == nil {
		t.Fatal("unsupported layout must error")
	}
}

// buildPCM makes a deterministic, non-silent interleaved waveform.
func buildPCM(total, channels int) []int16 {
	out := make([]int16, total)
	for i := range out {
		v := int16(((i*97 + 13) % 4001) - 2000)
		if v == 0 {
			v = 7
		}
		out[i] = v
	}
	return out
}
