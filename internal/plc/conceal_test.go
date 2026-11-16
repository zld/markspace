package plc

import (
	"testing"
)

func TestConcealDecaysThenSilences(t *testing.T) {
	c := New(4)
	real := []int16{1000, -1000, 1000, -1000}
	c.Output(real, true)

	// Three concealed frames: decaying copies of the real frame.
	prevGain := 1.0
	for i := 1; i <= MaxConsecutiveFrames; i++ {
		out := c.Output(nil, false)
		prevGain *= decay
		for j, s := range out {
			want := int16(float64(real[j]) * prevGain)
			if s != want {
				t.Fatalf("frame %d sample %d = %d, want %d", i, j, s, want)
			}
		}
	}
	// Fourth consecutive miss: silence.
	out := c.Output(nil, false)
	for j, s := range out {
		if s != 0 {
			t.Fatalf("silent frame sample %d = %d, want 0", j, s)
		}
	}
	if c.Concealed != 4 {
		t.Fatalf("Concealed = %d, want 4", c.Concealed)
	}
}

func TestConcealBeforeAnyRealFrameIsSilent(t *testing.T) {
	c := New(4)
	out := c.Output(nil, false)
	for j, s := range out {
		if s != 0 {
			t.Fatalf("sample %d = %d, want 0", j, s)
		}
	}
}

func TestRealFrameResetsMissRun(t *testing.T) {
	c := New(2)
	c.Output([]int16{100, 100}, true)
	c.Output(nil, false)
	c.Output(nil, false)
	c.Output([]int16{200, 200}, true) // real frame resets the run
	out := c.Output(nil, false)       // concealed again, not silence
	if out[0] != int16(200*decay) {
		t.Fatalf("concealed sample = %d, want %d", out[0], int16(200*decay))
	}
}

func TestContinuityRatio(t *testing.T) {
	c := New(2)
	c.Output([]int16{0, 0}, true)
	c.Output([]int16{100, 100}, true)     // jump 100 -> ok
	c.Output([]int16{20000, 20000}, true) // jump 19900 -> not ok
	if c.transitions != 2 || c.ok != 1 {
		t.Fatalf("transitions=%d ok=%d, want 2/1", c.transitions, c.ok)
	}
	if got := c.ContinuityRatio(); got != 0.5 {
		t.Fatalf("ContinuityRatio = %v, want 0.5", got)
	}
	if c.MaxJump != 19900 {
		t.Fatalf("MaxJump = %d, want 19900", c.MaxJump)
	}
}

func TestContinuityRatioNoTransitions(t *testing.T) {
	c := New(2)
	if got := c.ContinuityRatio(); got != 1 {
		t.Fatalf("ContinuityRatio = %v, want 1", got)
	}
}
