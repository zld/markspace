package clock

import "testing"

func TestRationalArithmeticIsExact(t *testing.T) {
	got := NewRational(1, 3).Add(NewRational(1, 6))
	if want := NewRational(1, 2); !got.Equal(want) {
		t.Fatalf("1/3+1/6=%s want %s", got, want)
	}
	if diff := NewRational(1, 10).Sub(NewRational(1, 100)); !diff.Equal(NewRational(9, 100)) {
		t.Fatalf("unexpected subtraction %s", diff)
	}
	if NewRational(-2, -4).Cmp(NewRational(1, 2)) != 0 {
		t.Fatal("sign normalization failed")
	}
}

func TestTimeBaseConversion(t *testing.T) {
	tb := TimeBase{TicksPerSecond: 48000}
	empty := NewRational(1, 10) // 100 ms in movie timescale 1000
	ticks, ok := tb.Ticks(empty)
	if !ok || ticks != 4800 {
		t.Fatalf("Ticks(1/10s)=%d,%v want 4800,true", ticks, ok)
	}
	oneTick := tb.Tick()
	if secs := tb.Time(4800); !secs.Equal(empty) {
		t.Fatalf("4800 ticks=%s want 1/10", secs)
	}
	if oneTick.Cmp(NewRational(1, 48000)) != 0 {
		t.Fatalf("tick duration=%s", oneTick)
	}
	if _, ok := tb.Ticks(NewRational(1, 7)); ok {
		t.Fatal("1/7 second should not be integral at 48 kHz")
	}
}
