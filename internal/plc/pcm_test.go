package plc

import "testing"

func frame(v int16) []int16 {
	out := make([]int16, FrameSamples)
	for i := range out {
		out[i] = v
	}
	return out
}

func TestConcealDecays(t *testing.T) {
	c := New(1)
	c.Real(frame(1000))
	out := make([]int16, FrameSamples)
	c.Conceal(out)
	if out[0] != 800 {
		t.Fatalf("first conceal = %d, want 800", out[0])
	}
	c.Conceal(out)
	if out[0] != 512 {
		t.Fatalf("second conceal = %d, want 512 (compounded 0.8^2)", out[0])
	}
}

func TestConcealSwitchesToNoise(t *testing.T) {
	c := New(1)
	c.Real(frame(30000))
	out := make([]int16, FrameSamples)
	for i := 0; i < c.NoiseAfter; i++ {
		c.Conceal(out)
	}
	c.Conceal(out) // noise frame
	for i, s := range out {
		if s > 200 || s < -200 {
			t.Fatalf("noise sample %d = %d out of range", i, s)
		}
	}
	// Deterministic for the same seed.
	c2 := New(1)
	c2.Real(frame(30000))
	out2 := make([]int16, FrameSamples)
	for i := 0; i <= c2.NoiseAfter; i++ {
		c2.Conceal(out2)
	}
	if out[0] != out2[0] || out[FrameSamples-1] != out2[FrameSamples-1] {
		t.Fatal("noise not deterministic")
	}
}

func TestRealReportsResumeJump(t *testing.T) {
	c := New(1)
	c.Real(frame(1000))
	out := make([]int16, FrameSamples)
	c.Conceal(out) // last output sample = 800
	jump, resumed := c.Real(frame(2000))
	if !resumed {
		t.Fatal("expected resumed=true")
	}
	if jump != 1200 {
		t.Fatalf("jump=%d, want 1200", jump)
	}
	if _, resumed := c.Real(frame(2000)); resumed {
		t.Fatal("second real frame must not report resume")
	}
}
