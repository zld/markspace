package audio

import (
	"math"
	"testing"

	"mediapipe/internal/model"
)

func TestMonoAndStereo(t *testing.T) {
	mono, name, err := Normalize([]float64{0.5, -0.25}, 1)
	if err != nil || name != "mono->stereo" {
		t.Fatalf("mono: %v %s", err, name)
	}
	// Each mono sample appears identically on L and R.
	want := []float64{0.5, 0.5, -0.25, -0.25}
	for i := range want {
		if math.Abs(mono[i]-want[i]) > 1e-12 {
			t.Fatalf("mono out = %v", mono)
		}
	}

	st, _, err := Normalize([]float64{0.1, 0.2, 0.3, 0.4}, 2)
	if err != nil {
		t.Fatal(err)
	}
	if len(st) != 4 || st[0] != 0.1 || st[3] != 0.4 {
		t.Fatalf("stereo passthrough altered data: %v", st)
	}
}

func TestFiveOneDownmix(t *testing.T) {
	// One 5.1 frame: L=1 R=-1 C=0.4 LFE=1 (must be dropped) Ls=0.2 Rs=-0.2.
	in := []float64{1, -1, 0.4, 1, 0.2, -0.2}
	out, name, err := Normalize(in, 6)
	if err != nil || name != "5.1->stereo" {
		t.Fatalf("5.1: %v %s", err, name)
	}
	if len(out) != 2 {
		t.Fatalf("output length = %d", len(out))
	}
	expL := 1.0 + 0.7071*0.4 + 0.7071*0.2
	expR := -1.0 + 0.7071*0.4 + 0.7071*(-0.2)
	if math.Abs(out[0]-expL) > 1e-9 || math.Abs(out[1]-expR) > 1e-9 {
		t.Fatalf("downmix = %v, want %v %v", out, expL, expR)
	}
}

func TestLayoutRejects(t *testing.T) {
	if _, _, err := Normalize([]float64{1, 2, 3}, 4); model.AsMediaError(err) == nil {
		t.Fatal("4 channels must be rejected")
	}
	if _, _, err := Normalize([]float64{1, 2, 3}, 2); model.AsMediaError(err) == nil {
		t.Fatal("non-frame-aligned PCM must be rejected")
	}
	mtx, _ := MatrixFor(2)
	if _, err := mtx.Apply(Frame{1}); model.AsMediaError(err) == nil {
		t.Fatal("matrix arity mismatch must be rejected")
	}
}
