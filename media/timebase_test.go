package media

import "testing"

func TestScaleKeepsExactRemainder(t *testing.T) {
	// 6.006s at 90kHz is exactly 540540 ticks; at 1kHz it is 6006/1000.
	tr, err := ParseDecimalSeconds("6.006")
	if err != nil {
		t.Fatal(err)
	}
	if tr.Num != 3003 || tr.Den != 500 {
		t.Fatalf("ratio=%v", tr)
	}
	tt := NewTime(540540, Timescale90K)
	q, rem, err := tt.Scale(TimescaleMS)
	if err != nil {
		t.Fatal(err)
	}
	if q != 6006 {
		t.Fatalf("ticks=%d", q)
	}
	if !rem.IsZero() {
		t.Fatalf("expected exact ms remainder, got %v", rem)
	}
}

func TestScaleReportsLeftover(t *testing.T) {
	// One tick at 90kHz is not representable in milliseconds.
	tt := NewTime(1, Timescale90K)
	_, rem, err := tt.Scale(TimescaleMS)
	if err != nil {
		t.Fatal(err)
	}
	if rem.IsZero() {
		t.Fatal("expected nonzero remainder")
	}
	// The remainder in seconds must be exactly 1/90000.
	if rem.Num != 1 || rem.Den != 90000 {
		t.Fatalf("rem=%v", rem)
	}
}

func TestAlignAccumulatorSumsExactly(t *testing.T) {
	// A 1001/30000-second tick leaves 11/30000s when truncated to ms.
	var acc AlignAccumulator
	tt := NewTime(1001, 30000)
	for i := 0; i < 3; i++ {
		_, rem, err := tt.Scale(TimescaleMS)
		if err != nil {
			t.Fatal(err)
		}
		acc.Observe(rem)
	}
	if acc.Sum.Num != 11 || acc.Sum.Den != 10000 {
		t.Fatalf("sum=%v", acc.Sum)
	}
	if acc.ErrorMS() != 2 {
		t.Fatalf("ceil ms=%d", acc.ErrorMS())
	}
	if acc.Samples != 3 {
		t.Fatalf("samples=%d", acc.Samples)
	}
}

func TestParseDecimalSeconds(t *testing.T) {
	cases := []struct {
		in  string
		num int64
		den int64
		ok  bool
	}{
		{"6", 6, 1, true},
		{"6.006", 3003, 500, true},
		{"0.0334", 167, 5000, true},
		{" 1.5 ", 3, 2, true},
		{"1e3", 0, 0, false},
		{"-1", 0, 0, false},
		{"", 0, 0, false},
		{"1.2.3", 0, 0, false},
	}
	for _, c := range cases {
		r, err := ParseDecimalSeconds(c.in)
		if c.ok != (err == nil) {
			t.Fatalf("%q: err=%v", c.in, err)
		}
		if !c.ok {
			continue
		}
		if r.Num != c.num || r.Den != c.den {
			t.Fatalf("%q: got %v", c.in, r)
		}
	}
}

func TestAddIsExactAcrossTimescales(t *testing.T) {
	a := NewTime(1, 3) // 1/3 s
	b := NewTime(1, 6) // 1/6 s
	got, rem, err := a.Add(b)
	if err != nil {
		t.Fatal(err)
	}
	// 1/3s is 0.333... ticks at ts=3, plus exactly 1/6s leftover.
	if got.Value != 1 || got.Timescale != 3 {
		t.Fatalf("got=%v", got)
	}
	want := NewRatio(1, 6)
	if rem.Cmp(want) != 0 {
		t.Fatalf("rem=%v want %v", rem, want)
	}
}

func TestRatioNormalisation(t *testing.T) {
	r := NewRatio(2, -4)
	if r.Num != -1 || r.Den != 2 {
		t.Fatalf("r=%v", r)
	}
	if (Ratio{}).Cmp(NewRatio(0, 5)) != 0 {
		t.Fatal("zero ratios must compare equal")
	}
	if NewRatio(1, 0) != (Ratio{}) {
		t.Fatal("zero denominator must yield zero ratio")
	}
}

func TestCeilMSNeverUnderstates(t *testing.T) {
	r := NewRatio(1, 30000) // 0.0333ms
	if got := r.CeilMS(); got != 1 {
		t.Fatalf("ceil=%d", got)
	}
	if got := NewRatio(-1, 30000).CeilMS(); got != -1 {
		t.Fatalf("ceil negative=%d", got)
	}
}
