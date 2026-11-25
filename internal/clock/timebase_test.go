package clock

import "testing"

func TestParseRate(t *testing.T) {
	cases := []struct {
		tok string
		ok  bool
		df  bool
	}{
		{"24", true, false},
		{"25", true, false},
		{"50", true, false},
		{"30000/1001-ndf", true, false},
		{"30000/1001-df", true, true},
		{"29.97-df", true, true},
		{"29.97-ndf", true, false},
		{"30", false, false},       // undeclared integer rate
		{"24-df", false, false},    // DF marker on non-DF rate
		{"23.976", false, false},   // unsupported decimal
		{"30000/0", false, false},  // zero denominator
		{"abc", false, false},      // garbage
		{"30000/1001", true, false}, // fractional defaults to NDF
	}
	for _, c := range cases {
		r, err := ParseRate(c.tok)
		if c.ok && err != nil {
			t.Errorf("ParseRate(%q) unexpected error: %v", c.tok, err)
			continue
		}
		if !c.ok && err == nil {
			t.Errorf("ParseRate(%q) expected error, got %v", c.tok, r)
			continue
		}
		if c.ok && r.DF != c.df {
			t.Errorf("ParseRate(%q) DF=%v want %v", c.tok, r.DF, c.df)
		}
	}
}

func TestDropFrameTimecode(t *testing.T) {
	r, err := ParseRate("30000/1001-df")
	if err != nil {
		t.Fatal(err)
	}
	// 00:01:00;02 is the first legal label after the 00:00:59;29 drop.
	tc, err := ParseTimecode("00:01:00;02", r)
	if err != nil {
		t.Fatalf("legal DF label rejected: %v", err)
	}
	// 00:00:59;29 = frame 1799; next label 00:01:00;02 must be frame 1800.
	prev, err := ParseTimecode("00:00:59;29", r)
	if err != nil {
		t.Fatal(err)
	}
	if got := tc.Frames() - prev.Frames(); got != 1 {
		t.Errorf("DF skip: frame delta=%d want 1", got)
	}
	// Labels landing inside the dropped window are illegal.
	if _, err := ParseTimecode("00:01:00;00", r); err == nil {
		t.Error("dropped frame label 00:01:00;00 must be rejected")
	}
	// The tenth minute drops nothing: 00:10:00;00 is legal.
	if _, err := ParseTimecode("00:10:00;00", r); err != nil {
		t.Errorf("ten-minute boundary rejected: %v", err)
	}
}

func TestNDFTimecodeAndInstant(t *testing.T) {
	r, _ := ParseRate("25")
	tc, err := ParseTimecode("00:00:01:00", r)
	if err != nil {
		t.Fatal(err)
	}
	if got := tc.Frames(); got != 25 {
		t.Errorf("frames=%d want 25", got)
	}
	got := r.Instant(tc.Frames())
	want := MustRat(1, 1)
	if got.Cmp(want) != 0 {
		t.Errorf("instant=%s want %s", got, want)
	}
	// Colon-separated DF label on an NDF rate is an error.
	if _, err := ParseTimecode("00:01:00;02", r); err == nil {
		t.Error("NDF rate must reject ';' separator")
	}
}

func TestFrameCountAcrossRates(t *testing.T) {
	// One nominal second holds the advertised frame count at every rate.
	for _, tok := range []string{"24", "25", "50", "30000/1001-ndf"} {
		r, _ := ParseRate(tok)
		tc, err := ParseTimecode("00:00:01:00", r)
		if err != nil {
			t.Fatalf("%s: %v", tok, err)
		}
		if r.Den == 1 && tc.Frames() != int64(r.Num) {
			t.Errorf("%s: 1s frames=%d want %d", tok, tc.Frames(), r.Num)
		}
		if r.Instant(tc.Frames()).Cmp(r.FrameDuration().Mul(tc.Frames())) != 0 {
			t.Errorf("%s: one-second instant mismatch", tok)
		}
	}
}

func TestIntervalWraparound(t *testing.T) {
	r, _ := ParseRate("25")
	if _, err := r.Interval(120, 60); err == nil {
		t.Error("inverted frame interval must error")
	}
	iv, err := r.Interval(0, 50)
	if err != nil {
		t.Fatal(err)
	}
	if iv.Frames != 50 || iv.Start.Cmp(MustRat(0, 1)) != 0 ||
		iv.End.Cmp(MustRat(2, 1)) != 0 {
		t.Errorf("interval mismatch: %+v", iv)
	}
}

func TestRatArithmetic(t *testing.T) {
	a := MustRat(1, 3)
	b := MustRat(1, 6)
	if a.Add(b).Cmp(MustRat(1, 2)) != 0 {
		t.Error("rat add failed")
	}
	if a.Sub(b).Cmp(MustRat(1, 6)) != 0 {
		t.Error("rat sub failed")
	}
	if MustRat(2, -4).Den() <= 0 {
		t.Error("denominator must be normalized positive")
	}
	if _, err := NewRat(1, 0); err == nil {
		t.Error("zero denominator must error")
	}
}
