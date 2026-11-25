package audio

import (
	"errors"
	"testing"

	"replay/internal/model"
)

func TestPlanAudioPassthrough(t *testing.T) {
	p, err := PlanAudio(model.AudioInfo{Channels: 2, SampleRate: 48000})
	if err != nil {
		t.Fatal(err)
	}
	if p.Resample || p.Layout != "stereo" || p.SampleRate != 48000 {
		t.Fatalf("unexpected plan: %+v", p)
	}
}

func TestPlanAudioResample(t *testing.T) {
	p, err := PlanAudio(model.AudioInfo{Channels: 2, SampleRate: 44100})
	if err != nil {
		t.Fatal(err)
	}
	if !p.Resample || p.SampleRate != TargetSampleRate {
		t.Fatalf("expected resample to %d: %+v", TargetSampleRate, p)
	}
	if p.Input.SampleRate != 44100 {
		t.Fatal("input must be preserved in the plan")
	}
}

func TestPlanAudioMonoKept(t *testing.T) {
	p, err := PlanAudio(model.AudioInfo{Channels: 1, SampleRate: 48000})
	if err != nil {
		t.Fatal(err)
	}
	if p.Layout != "mono" || p.Channels != 1 {
		t.Fatalf("mono must be kept: %+v", p)
	}
}

func TestPlanAudioUnsupportedFails(t *testing.T) {
	_, err := PlanAudio(model.AudioInfo{Channels: 6, SampleRate: 48000})
	if !errors.Is(err, model.ErrUnsupportedLayout) {
		t.Fatalf("want ErrUnsupportedLayout, got %v", err)
	}
	if _, err := PlanAudio(model.AudioInfo{Channels: 2, SampleRate: 0}); !errors.Is(err, model.ErrUnsupportedLayout) {
		t.Fatalf("zero rate: want ErrUnsupportedLayout, got %v", err)
	}
}

func TestLayoutName(t *testing.T) {
	if n, ok := LayoutName(1); !ok || n != "mono" {
		t.Fatal("mono")
	}
	if n, ok := LayoutName(2); !ok || n != "stereo" {
		t.Fatal("stereo")
	}
	if _, ok := LayoutName(8); ok {
		t.Fatal("8 channels must be unsupported")
	}
}
