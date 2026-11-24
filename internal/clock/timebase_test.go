package clock

import (
	"math"
	"testing"
)

func TestRationalArithmeticAndAlignment(t *testing.T) {
	frameTB := New(1, 24000)
	b := New(1000, 24000) // one frame
	if got := Ticks(1000, frameTB); got.Cmp(b) != 0 {
		t.Fatalf("ticks = %v, want %v", got, b)
	}
	sum := New(1, 2).Add(New(1, 3))
	if sum.Cmp(New(5, 6)) != 0 {
		t.Fatalf("sum = %v", sum)
	}
	if ms := AlignmentErrorMS(New(1, 1000), New(0, 1)); math.Abs(ms-1) > 1e-12 {
		t.Fatalf("alignment ms = %v", ms)
	}
}

func TestConvertTicksAndExact(t *testing.T) {
	src := TickRate(24000)
	dst := TickRate(720000) // 30x LCM rate
	if got := ConvertTicks(1, src, dst); got != 30 {
		t.Fatalf("convert = %d, want 30", got)
	}
	got, err := ExactTicks(2, TickRate(48000), dst)
	if err != nil || got != 30 {
		t.Fatalf("exact = %d,%v", got, err)
	}
	if _, err := ExactTicks(1, TickRate(24000), TickRate(48000)); err != nil {
		t.Fatalf("24k to 48k should be exact: %v", err)
	}
	if _, err := ExactTicks(1, TickRate(24000), TickRate(44100)); err == nil {
		t.Fatal("expected non-integral conversion error")
	}
}

func TestCommonTimebase(t *testing.T) {
	got, err := CommonTimebase(TickRate(24000), TickRate(48000), TickRate(90000))
	if err != nil {
		t.Fatal(err)
	}
	want := TickRate(720000)
	if !got.Equal(want) {
		t.Fatalf("common = %v, want %v", got, want)
	}
	for _, tc := range []struct{ n, d int64 }{
		{0, 0}, {1, 0}, {1, -1},
	} {
		err := (Rational{Num: tc.n, Den: tc.d}).Validate()
		if err == nil {
			t.Fatalf("invalid rational accepted: %+v", tc)
		}
	}
}
