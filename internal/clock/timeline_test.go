package clock

import "testing"

func TestScaleExactDivisions(t *testing.T) {
	cases := []struct {
		value, src, want int64
	}{
		{48000, 48000, 90000}, // one second of audio
		{90000, 90000, 90000},
		{1000, 1000, 90000}, // one second at ms scale
		{3000, 30, 9_000_000}, // 100 s of 30 fps frames
		{0, 48000, 0},
	}
	for _, c := range cases {
		got, err := Scale(c.value, c.src)
		if err != nil {
			t.Fatalf("Scale(%d,%d) unexpected error %v", c.value, c.src, err)
		}
		if got != c.want {
			t.Errorf("Scale(%d,%d) = %d, want %d", c.value, c.src, got, c.want)
		}
	}
}

func TestScaleRounding(t *testing.T) {
	// 1 tick at 44.1 kHz -> 2.040... master ticks, rounds to 2.
	got, err := Scale(1, 44100)
	if err != nil || got != 2 {
		t.Fatalf("Scale(1,44100) = %d, %v want 2", got, err)
	}
	// Negative value must round away from zero symmetrically.
	neg, err := Scale(-1, 44100)
	if err != nil || neg != -2 {
		t.Fatalf("Scale(-1,44100) = %d, %v want -2", neg, err)
	}
}

func TestScaleRejectsBadRate(t *testing.T) {
	if _, err := Scale(10, 0); err != ErrInvalidRate {
		t.Fatalf("zero rate: want ErrInvalidRate, got %v", err)
	}
	if _, err := ScaleTo(10, 1000, -1); err != ErrInvalidRate {
		t.Fatalf("negative dst rate: want ErrInvalidRate, got %v", err)
	}
}

func TestRoundTrip(t *testing.T) {
	// Master -> 48k -> master loses at most one source tick of precision.
	for _, v := range []int64{0, 1, 90, 45000, 89999, 90000, 1234567} {
		audio, err := FromMaster(v, 48000)
		if err != nil {
			t.Fatal(err)
		}
		back, err := Scale(audio, 48000)
		if err != nil {
			t.Fatal(err)
		}
		if d := back - v; d > 2 || d < -2 {
			t.Errorf("round trip %d -> %d -> %d drift %d", v, audio, back, d)
		}
	}
}

func TestDurationHelpers(t *testing.T) {
	if Ticks(1) != 90 {
		t.Errorf("Ticks(1ms) = %d, want 90", Ticks(1))
	}
	if Millis(90) != 1 {
		t.Errorf("Millis(90) = %d, want 1", Millis(90))
	}
	if Micros(9) != 100 {
		t.Errorf("Micros(9) = %d, want 100", Micros(9))
	}
}
