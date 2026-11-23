package audio

import (
	"math"
	"testing"
)

func TestParseLayout(t *testing.T) {
	if l, err := ParseLayout(1); err != nil || l.Name != LayoutMono {
		t.Fatalf("mono: %+v %v", l, err)
	}
	if l, err := ParseLayout(2); err != nil || l.Name != LayoutStereo {
		t.Fatalf("stereo: %+v %v", l, err)
	}
	if _, err := ParseLayout(6); err == nil {
		t.Fatal("expected error for 5.1")
	}
}

func TestNormalizeMonoToStereo(t *testing.T) {
	in := []float64{1, -1, 0.5}
	out, err := Normalize(in, 1, Layout{Name: LayoutStereo, Channels: 2})
	if err != nil {
		t.Fatal(err)
	}
	if len(out) != 6 {
		t.Fatalf("len=%d", len(out))
	}
	for i := range in {
		if math.Abs(out[2*i]-in[i]*0.7071067811865476) > 1e-9 || out[2*i] != out[2*i+1] {
			t.Fatalf("frame %d = %v", i, out[2*i:2*i+2])
		}
	}
}

func TestNormalizeStereoToMonoIsPowerPreserving(t *testing.T) {
	// Identical channels must keep amplitude, unlike a plain sum.
	in := []float64{0.8, 0.8, -0.4, -0.4}
	out, err := Normalize(in, 2, Layout{Name: LayoutMono, Channels: 1})
	if err != nil {
		t.Fatal(err)
	}
	want := []float64{0.8, -0.4}
	for i := range want {
		if math.Abs(out[i]-want[i]) > 1e-12 {
			t.Fatalf("out=%v want=%v", out, want)
		}
	}
}

func TestNormalizeRejectsUnknown(t *testing.T) {
	if _, err := Normalize([]float64{1}, 3, Layout{Name: LayoutMono, Channels: 1}); err == nil {
		t.Fatal("expected error")
	}
	if got := ChannelCount(LayoutStereo); got != 2 {
		t.Fatalf("count=%d", got)
	}
}
