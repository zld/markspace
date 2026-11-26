package clock

import "testing"

func TestParseDecimalExact(t *testing.T) {
	cases := map[string]Rational{
		"0.5":       {Num: 1, Den: 2},
		"0.500000":  {Num: 1, Den: 2},
		"2.005":     {Num: 401, Den: 200},
		"10":        {Num: 10, Den: 1},
		"0.1":       {Num: 1, Den: 10},
		"123.00025": {Num: 492001, Den: 4000},
	}
	for in, want := range cases {
		got, err := ParseDecimal(in)
		if err != nil {
			t.Fatalf("ParseDecimal(%q): %v", in, err)
		}
		if got != want {
			t.Fatalf("ParseDecimal(%q) = %v, want %v", in, got, want)
		}
	}
}

func TestParseDecimalRejectsMalformed(t *testing.T) {
	for _, in := range []string{"", "-1", "+1", "1.", ".5", "1.2.3", "1e3", "a.b"} {
		if _, err := ParseDecimal(in); err == nil {
			t.Fatalf("ParseDecimal(%q) unexpectedly succeeded", in)
		}
	}
}

func TestRescaleSingleStep(t *testing.T) {
	tm := Must(24000, 48000)
	got := tm.Rescale(90000)
	if got.Ticks != 45000 || got.Timescale != 90000 {
		t.Fatalf("Rescale = %+v", got)
	}
	back := got.Rescale(48000)
	if back.Ticks != 24000 {
		t.Fatalf("round trip = %+v", back)
	}
}

func TestAccumulationIsExact(t *testing.T) {
	seg, _ := ParseDecimal("0.1")
	var boundary Rational
	var ticks []int64
	for i := 0; i < 1000; i++ {
		ticks = append(ticks, boundary.Ticks(90000))
		boundary = boundary.Add(seg)
	}
	if ticks[0] != 0 || ticks[999] != 8991000 {
		t.Fatalf("unexpected boundaries: first=%d last=%d", ticks[0], ticks[999])
	}
	if boundary.Compare(MustRational(100, 1)) != 0 {
		t.Fatalf("1000 x 0.1 != 100: %v", boundary)
	}
}

func TestRoundHalfAwayFromZero(t *testing.T) {
	if got := (Rational{Num: 1, Den: 2}).Ticks(1); got != 1 {
		t.Fatalf("half = %d", got)
	}
	if got := (Rational{Num: -1, Den: 2}).Ticks(1); got != -1 {
		t.Fatalf("negative half = %d", got)
	}
	if got := (Rational{Num: 1, Den: 3}).Ticks(3); got != 1 {
		t.Fatalf("third = %d", got)
	}
}

func TestCompareAcrossTimescales(t *testing.T) {
	a := Must(90000, 90000)
	b := Must(48000, 48000)
	if a.Compare(b) != 0 {
		t.Fatalf("1s at 90k != 1s at 48k")
	}
	if Must(1, 90000).Compare(Must(1, 48000)) >= 0 {
		t.Fatalf("1/90000 should be before 1/48000")
	}
}
