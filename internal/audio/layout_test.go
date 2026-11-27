package audio

import (
	"math"
	"testing"
)

func TestNormalizeMonoToStereo(t *testing.T) {
	in := Buffer{Samples: []float64{0.5, -0.25}, Frames: 2, Ch: 1}
	out, err := Normalize(in, LayoutStereo)
	if err != nil {
		t.Fatal(err)
	}
	want := []float64{0.5, 0.5, -0.25, -0.25}
	for i := range want {
		if math.Abs(out.Samples[i]-want[i]) > 1e-12 {
			t.Fatalf("sample %d = %v, want %v", i, out.Samples[i], want[i])
		}
	}
}

func TestNormalizeStereoToMono(t *testing.T) {
	in := Buffer{Samples: []float64{0.8, 0.2, -0.4, 0.4}, Frames: 2, Ch: 2}
	out, err := Normalize(in, LayoutMono)
	if err != nil {
		t.Fatal(err)
	}
	if out.Ch != 1 || out.Frames != 2 {
		t.Fatalf("shape = %dx%d, want 2x1", out.Frames, out.Ch)
	}
	if math.Abs(out.Samples[0]-0.5) > 1e-12 || math.Abs(out.Samples[1]) > 1e-12 {
		t.Fatalf("downmix = %v, want [0.5 0]", out.Samples)
	}
}

func TestNormalizePassthroughAndReject(t *testing.T) {
	in := Buffer{Samples: []float64{1, 2}, Frames: 1, Ch: 2}
	out, err := Normalize(in, LayoutStereo)
	if err != nil || &out.Samples[0] != &in.Samples[0] {
		t.Fatal("matching layout should pass through without copy")
	}
	if _, err := Normalize(in, Layout("quad")); err == nil {
		t.Fatal("expected error for unsupported layout")
	}
}
