package timeline

import "testing"

func TestNsFromUnits(t *testing.T) {
	cases := []struct {
		units, rate, want int64
	}{
		{0, 48000, 0},
		{1, 48000, 20833}, // 1e9/48000 = 20833.333 -> 20833
		{2, 48000, 41667}, // 41666.667 -> 41667 (half away from zero)
		{24, 24, 1_000_000_000},
		{1, 1000, 1_000_000},
	}
	for _, c := range cases {
		got, ok := NsFromUnits(c.units, c.rate)
		if !ok || got != c.want {
			t.Fatalf("NsFromUnits(%d,%d)=%d,%v want %d", c.units, c.rate, got, ok, c.want)
		}
		back, ok := UnitsFromNs(got, c.rate)
		if !ok || back != c.units {
			t.Fatalf("UnitsFromNs roundtrip=%d,%v want %d", back, ok, c.units)
		}
	}
	if _, ok := NsFromUnits(1, 0); ok {
		t.Fatal("zero rate must fail")
	}
}

func TestRationalMulOverflow(t *testing.T) {
	r := Rational{Num: 1 << 62, Den: 1}
	if _, ok := r.Mul(8); ok {
		t.Fatal("expected overflow rejection")
	}
	if got, ok := (Rational{Num: 5, Den: 2}).Mul(3); !ok || got != 8 {
		t.Fatalf("5/2*3 = %d,%v want 8", got, ok)
	}
}

func TestErrorContext(t *testing.T) {
	err := New(CatCorrupt, "truncated element").WithOffset(4096).WithTrack(7)
	if !IsCategory(err, CatCorrupt) || IsCategory(err, CatIO) {
		t.Fatal("category lookup failed")
	}
	if err.Error() != "corrupt: truncated element track=7 offset=4096" {
		t.Fatalf("unexpected message %q", err.Error())
	}
	if err.Offset != 4096 || err.TrackUID != 7 {
		t.Fatal("context lost")
	}
}
