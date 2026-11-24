package metadata

import (
	"testing"

	"github.com/example/tsrepair/internal/model"
)

func TestAudioRateAndLayoutRemap(t *testing.T) {
	n := NewAudioNormalizer(DefaultAudioConfig())
	out, err := n.Normalize(AudioDescriptor{
		PID: 0x102, Layout: LayoutSurround, Channels: 6,
		SampleRate: 44100, FrameSizeBytes: 4}, 188)
	if err != nil {
		t.Fatalf("normalize: %v", err)
	}
	if !out.RateRemapped || out.SampleRate != 48000 {
		t.Fatalf("rate remap wrong: %+v", out)
	}
	if !out.LayoutRemapped || out.Layout != LayoutStereo {
		t.Fatalf("layout remap wrong: %+v", out)
	}
	kinds := map[string]bool{}
	for _, e := range n.Events() {
		kinds[e.Kind] = true
	}
	if !kinds["rate-conflict"] || !kinds["layout-remap"] {
		t.Fatalf("events missing: %v", kinds)
	}
}

func TestAudioCanonicalValuesPass(t *testing.T) {
	n := NewAudioNormalizer(DefaultAudioConfig())
	out, err := n.Normalize(AudioDescriptor{
		PID: 0x102, Layout: LayoutStereo, Channels: 2, SampleRate: 48000}, 0)
	if err != nil || out.RateRemapped || out.LayoutRemapped {
		t.Fatalf("canonical descriptor must pass untouched: %+v err=%v", out, err)
	}
}

func TestAudioUnknownLayoutAndRateAreLocatableErrors(t *testing.T) {
	n := NewAudioNormalizer(DefaultAudioConfig())
	_, err := n.Normalize(AudioDescriptor{
		PID: 0x102, Layout: LayoutUnknown, SampleRate: 48000}, 752)
	if err == nil || model.IsFatal(err) {
		t.Fatalf("unknown layout must be a recoverable error, got %v", err)
	}
	if err.PID != 0x102 || err.Offset != 752 {
		t.Fatalf("error must carry pid and offset: %v", err)
	}
	_, err = n.Normalize(AudioDescriptor{
		PID: 0x102, Layout: LayoutStereo, SampleRate: 12345}, 0)
	if err == nil {
		t.Fatal("unmapped sample rate must be rejected, not silently dropped")
	}
	if n.ErrorCount() != 2 {
		t.Fatalf("error count=%d want 2", n.ErrorCount())
	}
}

func TestAudioDivisibilityCheck(t *testing.T) {
	n := NewAudioNormalizer(DefaultAudioConfig())
	if err := n.CheckDivisible(0x102, 128, 4, 0); err != nil {
		t.Fatalf("divisible payload rejected: %v", err)
	}
	err := n.CheckDivisible(0x102, 130, 4, 376)
	if err == nil {
		t.Fatal("indivisible payload must error")
	}
	se := model.AsStreamError(err)
	if se == nil || se.Stage != model.StageMetadata || se.Offset != 376 {
		t.Fatalf("indivisible error shape wrong: %v", err)
	}
}
