package clock

import "testing"

func TestMulDivRound(t *testing.T) {
	cases := []struct {
		x, num, den, want int64
	}{
		{0, 48000, 48000, 0},
		{960, 44100, 48000, 882},
		{1, 1, 2, 1},
		{1, 1, 3, 0},
		{2, 1, 3, 1},
		{-1, 1, 2, -1},
		{-1, 1, 3, 0},
		{-2, 1, 3, -1},
	}
	for _, c := range cases {
		if got := MulDivRound(c.x, c.num, c.den); got != c.want {
			t.Fatalf("MulDivRound(%d,%d,%d)=%d want %d", c.x, c.num, c.den, got, c.want)
		}
	}
}

func TestRat(t *testing.T) {
	a := NewRat(960, 48000)
	b := NewRat(1, 50)
	if a.Cmp(b) != 0 {
		t.Fatalf("%v != %v", a, b)
	}
	if got := a.Add(b); got.Num != 1 || got.Den != 25 {
		t.Fatalf("add = %v", got)
	}
	if got := a.Sub(NewRat(1, 100)); got.Num != 1 || got.Den != 100 {
		t.Fatalf("sub = %v", got)
	}
}

func TestTimeSamplesAt(t *testing.T) {
	// 44100 Hz instant 1 sample into 48000 Hz: 48000/44100 = 1.088 -> 1.
	if got := NewTime(1, 44100).SamplesAt(48000); got != 1 {
		t.Fatalf("got %d", got)
	}
	// Exactly one second stays exact at both rates.
	one := NewTime(48000, 48000)
	if got := one.SamplesAt(44100); got != 44100 {
		t.Fatalf("got %d", got)
	}
	// 7 samples at 44.1k to 48k = 7.619 -> 8.
	if got := NewTime(7, 44100).SamplesAt(48000); got != 8 {
		t.Fatalf("got %d", got)
	}
}

func TestUnwrapper32Wrap(t *testing.T) {
	u := &Unwrapper32{}
	if got := u.Push(0xFFFFFF00); got != 0 {
		t.Fatalf("first = %d", got)
	}
	if got := u.Push(0x00000000); got != 256 {
		t.Fatalf("wrap 0 = %d want 256", got)
	}
	if got := u.Push(0x00000100); got != 512 {
		t.Fatalf("wrap 1 = %d want 512", got)
	}
	if got := u.Push(0x00000020); got != 288 {
		t.Fatalf("late reorder = %d want 288", got)
	}
}

func TestDelta(t *testing.T) {
	if got := Delta32(0xFFFFFFF0, 0x10); got != 32 {
		t.Fatalf("delta32 = %d", got)
	}
	if got := Delta16(65530, 5); got != 11 {
		t.Fatalf("delta16 = %d", got)
	}
}
