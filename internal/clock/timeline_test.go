package clock

import "testing"

func TestRationalAddExact(t *testing.T) {
	// 1/3 + 1/6 == 1/2 exactly; no float involved.
	a, b := Must(1, 3), Must(1, 6)
	s, err := a.Add(b)
	if err != nil {
		t.Fatal(err)
	}
	if s.Num != 1 || s.Den != 2 {
		t.Fatalf("got %d/%d", s.Num, s.Den)
	}
}

func TestQuantizeRoundTrip(t *testing.T) {
	tl, _ := NewTimeline(48000)
	ticks, rem, err := tl.Quantize(Must(1, 3)) // 1/3 s = 16000 ticks
	if err != nil {
		t.Fatal(err)
	}
	if ticks != 16000 || !rem.IsZero() {
		t.Fatalf("ticks=%d rem=%v", ticks, rem)
	}
	// 1/7 s at 48k does not divide evenly; remainder must be < 1 tick.
	ticks, rem, err = tl.Quantize(Must(1, 7))
	if err != nil {
		t.Fatal(err)
	}
	exact := 48000.0 / 7.0
	if d := float64(ticks) - exact; d > 0.5 || d < -0.5 {
		t.Fatalf("ticks=%d off by %v", ticks, d)
	}
	if rem.IsZero() {
		t.Fatal("expected non-zero remainder")
	}
}

func TestAlignError(t *testing.T) {
	src, _ := NewTimeline(32000)
	dst, _ := NewTimeline(48000)
	n, err := AlignError(src, 33792, dst, 50688) // same instant
	if err != nil || n != 0 {
		t.Fatalf("n=%d err=%v", n, err)
	}
	n, err = AlignError(src, 33792, dst, 50689) // one dst tick off
	if err != nil || n != 1 {
		t.Fatalf("n=%d err=%v", n, err)
	}
}

func TestOverflowReported(t *testing.T) {
	big := Must(1<<62, 1)
	if _, err := big.Add(big); err == nil {
		t.Fatal("expected overflow error")
	}
	if _, err := New(1, 0); err == nil {
		t.Fatal("expected denominator error")
	}
}
