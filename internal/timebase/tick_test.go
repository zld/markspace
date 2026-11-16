package timebase

import "testing"

func TestMakeAndCmp(t *testing.T) {
	r, err := Make(2, 4)
	if err != nil || r.Num != 1 || r.Den != 2 {
		t.Fatalf("Make(2,4) = %+v, %v", r, err)
	}
	if _, err := Make(1, 0); err == nil {
		t.Fatal("zero denominator must error")
	}
	a, _ := Make(30000, 1000) // 30
	b, _ := Make(900, 30)    // 30
	if a.Cmp(b) != 0 {
		t.Fatalf("equal rationals compared unequal: %v %v", a, b)
	}
	c, _ := Make(1000, 3)
	if c.Cmp(a) <= 0 {
		t.Fatal("333.33 should be greater than 30")
	}
}

func TestSubAbsTicksToMs(t *testing.T) {
	// 3000 ticks @ 90000 = 33.333...ms ; 1 tick @ 3000 = 0.333...ms
	r1, err := TicksToMs(3000, 90000)
	if err != nil {
		t.Fatal(err)
	}
	r2, err := TicksToMs(1, 3000)
	if err != nil {
		t.Fatal(err)
	}
	if r1.Cmp(r2) <= 0 {
		t.Fatal("33.33ms must exceed 0.33ms")
	}
	d, err := r1.Sub(r2)
	if err != nil {
		t.Fatal(err)
	}
	if got := FormatMillis(d.Abs()); got != "33.000" {
		t.Fatalf("delta = %s, want 33.000", got)
	}
	if _, err := TicksToMs(1, 0); err == nil {
		t.Fatal("bad timescale must error")
	}
}

func TestFormatMillis(t *testing.T) {
	cases := map[string]string{
		"12.5ms":   "12.500",
		"1.25ms":   "1.250",
		"0.0005ms": "0.001", // rounds half up
		"0ms":      "0.000",
		"1000ms":   "1000.000",
	}
	for in, want := range cases {
		r, err := ParseDuration(in)
		if err != nil {
			t.Fatalf("parse %s: %v", in, err)
		}
		if got := FormatMillis(r); got != want {
			t.Errorf("FormatMillis(%s) = %s, want %s", in, got, want)
		}
	}
}

func TestOverflow(t *testing.T) {
	if _, ok := Add64(1<<62, 1<<62); ok {
		t.Fatal("Add64 overflow not detected")
	}
	if _, ok := Mul64(1<<62, 4); ok {
		t.Fatal("Mul64 positive overflow not detected")
	}
	if _, ok := Mul64(-(1 << 62), 4); ok {
		t.Fatal("Mul64 negative overflow not detected")
	}
	v, ok := Mul64(-(1 << 62), 2) // == MinInt64, valid
	if !ok || v != -(1 << 63) {
		t.Fatalf("MinInt64 product rejected: %d %v", v, ok)
	}
	v, ok = Mul64(100, 200)
	if !ok || v != 20000 {
		t.Fatalf("ordinary product failed: %d %v", v, ok)
	}
}

func TestParseDuration(t *testing.T) {
	r, err := ParseDuration("20ms")
	if err != nil || r.Cmp(Rational{Num: 20, Den: 1}) != 0 {
		t.Fatalf("20ms = %+v, %v", r, err)
	}
	r, err = ParseDuration("1.5s")
	if err != nil {
		t.Fatal(err)
	}
	want := Rational{Num: 1500, Den: 1}
	if r.Cmp(want) != 0 {
		t.Fatalf("1.5s = %v, want %v", r, want)
	}
	r, err = ParseDuration("250us")
	if err != nil || r.Cmp(Rational{Num: 1, Den: 4}) != 0 {
		t.Fatalf("250us = %+v, %v", r, err)
	}
	for _, bad := range []string{"", "12", "ms", "1.2.3ms", ".ms", "abc"} {
		if _, err := ParseDuration(bad); err == nil {
			t.Errorf("ParseDuration(%q) should fail", bad)
		}
	}
}
