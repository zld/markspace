package clock

import "testing"

func TestRationalReductionAndCompare(t *testing.T) {
	r, err := NewRational(4, 8)
	if err != nil {
		t.Fatal(err)
	}
	if r.Num != 1 || r.Den != 2 {
		t.Fatalf("not reduced: %v", r)
	}
	if r.Cmp(MustRational(2, 4)) != 0 {
		t.Fatal("equal rationals compare non-zero")
	}
	if MustRational(1, 3).Cmp(MustRational(1, 2)) != -1 {
		t.Fatal("1/3 should be less than 1/2")
	}
	if _, err := NewRational(1, 0); err == nil {
		t.Fatal("zero denominator must error")
	}
}

func TestTimeBaseConvertExactAndConflict(t *testing.T) {
	// 300 ticks at 1/90000 s = 1/300 s; in 1/300 base that is exactly 1 tick.
	tb, err := NewTimeBase(300, 1, 90000)
	if err != nil {
		t.Fatal(err)
	}
	tick, err := tb.Convert(MustRational(1, 300))
	if err != nil || tick != 1 {
		t.Fatalf("exact convert got %d, %v", tick, err)
	}
	// 1 tick at 1/90000 cannot be expressed in 1/1000 ticks uniquely.
	tb2, _ := NewTimeBase(1, 1, 90000)
	if _, err := tb2.Convert(MustRational(1, 1000)); err == nil {
		t.Fatal("fractional destination tick must be a time-base conflict")
	}
}

func TestToSecondsKeepsOriginalTick(t *testing.T) {
	tb, _ := NewTimeBase(1500, 1, 90000)
	sec := tb.ToSeconds()
	if sec.Num != 1 || sec.Den != 60 {
		t.Fatalf("unexpected seconds %v", sec)
	}
	if tb.Tick != 1500 {
		t.Fatal("original tick must be preserved")
	}
}
