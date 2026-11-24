package adapter

import (
	"testing"

	"medcheck/internal/clock"
)

func TestAACPreservesMultichannelMapAndTargetRate(t *testing.T) {
	a := NewAACV1()
	in := AudioInput{
		Channels: 6, Layout: "5.1", SampleRate: 44100,
		Target:  AudioSpec{Channels: 6, SampleRate: 48000},
		Samples: make([]int16, 6*441), // 10 ms at 44.1k
	}
	for i := range in.Samples {
		in.Samples[i] = int16((i % 97) * 10)
	}
	out, errs := a.Adapt(in)
	if out.Spec.Channels != 6 || out.Spec.Layout != "5.1" || out.Spec.SampleRate != 48000 {
		t.Fatalf("spec=%+v", out.Spec)
	}
	wantMap := []string{"FL", "FR", "FC", "LFE", "Ls", "Rs"}
	for i := range wantMap {
		if out.Spec.ChannelMap[i] != wantMap[i] {
			t.Fatalf("map=%v", out.Spec.ChannelMap)
		}
	}
	if !out.Resampled || out.OutputFrames != 480 {
		t.Fatalf("resampled=%v frames in=%d out=%d", out.Resampled, out.InputFrames, out.OutputFrames)
	}
	found := false
	for _, e := range errs {
		if e.Code == clock.CodeAudioChannelMapped {
			found = true
		}
	}
	if !found {
		t.Fatalf("missing mapping record: %v", errs)
	}
}

func TestAACRecordsSampleRateConflictAndUsesExplicitTarget(t *testing.T) {
	a := NewAACV1()
	in := AudioInput{
		Channels: 2, SampleRate: 44100, ContainerSampleRate: 22050,
		Target:  AudioSpec{Channels: 2, SampleRate: 48000},
		Samples: make([]int16, 2*100),
	}
	out, errs := a.Adapt(in)
	if out.Spec.SampleRate != 48000 {
		t.Fatalf("rate=%d", out.Spec.SampleRate)
	}
	var conflict *clock.MediaError
	for i := range errs {
		if errs[i].Code == clock.CodeSampleRateConflict {
			conflict = &errs[i]
		}
	}
	if conflict == nil || conflict.Source == "" {
		t.Fatalf("expected sourced conflict: %v", errs)
	}
}

func TestAACDownmixStereo(t *testing.T) {
	a := NewAACV1()
	in := AudioInput{Channels: 6, SampleRate: 48000, Target: AudioSpec{Channels: 2, SampleRate: 48000},
		Samples: []int16{1000, 2000, 3000, 4000, 5000, 6000}}
	out, _ := a.Adapt(in)
	if out.Spec.Channels != 2 || len(out.Samples) != 2 || !out.Downmixed {
		t.Fatalf("out=%+v samples=%v", out.Spec, out.Samples)
	}
}
