package model

import "testing"

func TestRateUnify(t *testing.T) {
	a, err := NewRate(90000)
	if err != nil {
		t.Fatal(err)
	}
	b, err := NewRate(48000)
	if err != nil {
		t.Fatal(err)
	}
	u, err := UnifyRate(a, b)
	if err != nil {
		t.Fatal(err)
	}
	// lcm(90000,48000) = 720000
	if u.Num != 720000 || u.Den != 1 {
		t.Fatalf("unified rate = %s, want 720000/1", u)
	}
}

func TestRescaleExact(t *testing.T) {
	r90, _ := NewRate(90000)
	r48, _ := NewRate(48000)
	// 900 ticks at 48k = 0.01875s; at 90k that is 1687.5 ticks -> inexact.
	if _, err := (Time{Ticks: 900, Rate: r48}).Rescale(r90); err == nil {
		t.Fatal("expected inexact rescale error")
	}
	// 1920 ticks at 48k == 3600 ticks at 90k exactly.
	got, err := (Time{Ticks: 1920, Rate: r48}).Rescale(r90)
	if err != nil {
		t.Fatal(err)
	}
	if got.Ticks != 3600 {
		t.Fatalf("got %d ticks, want 3600", got.Ticks)
	}
}

func TestRescaleBigTicks(t *testing.T) {
	r90, _ := NewRate(90000)
	r1, _ := NewRate(1)
	// 2^54 seconds at 1 Hz rescaled to 90kHz overflows int64 and must error.
	if _, err := (Time{Ticks: 1 << 54, Rate: r1}).Rescale(r90); err == nil {
		t.Fatal("expected overflow error")
	}
}

func TestCompareAcrossRates(t *testing.T) {
	r90, _ := NewRate(90000)
	r48, _ := NewRate(48000)
	a := MustTime(3600, r90) // 0.04s
	b := MustTime(1920, r48) // 0.04s
	c, err := a.Compare(b)
	if err != nil {
		t.Fatal(err)
	}
	if c != 0 {
		t.Fatalf("compare = %d, want 0", c)
	}
	d := MustTime(1921, r48)
	if c, _ := a.Compare(d); c != -1 {
		t.Fatalf("compare = %d, want -1", c)
	}
}

func TestRangeValidate(t *testing.T) {
	r90, _ := NewRate(90000)
	ok := Range{Start: MustTime(0, r90), End: MustTime(9000, r90)}
	if err := ok.Validate(); err != nil {
		t.Fatal(err)
	}
	bad := Range{Start: MustTime(9000, r90), End: MustTime(0, r90)}
	if err := bad.Validate(); err == nil {
		t.Fatal("expected inverted range error")
	}
}

func TestAddOverflow(t *testing.T) {
	r := Rate{Num: 1, Den: 1}
	if _, err := (Time{Ticks: 1 << 62, Rate: r}).Add(Time{Ticks: 1 << 62, Rate: r}); err == nil {
		t.Fatal("expected overflow")
	}
}

func TestErrorExitCodes(t *testing.T) {
	cases := []struct {
		code Code
		want int
	}{
		{CodeTruncatedBox, 2},
		{CodeInterrupted, 75},
		{CodeInvalidInput, 64},
		{CodeDigestMismatch, 1},
	}
	for _, c := range cases {
		e := E(c.code, "op", "detail").WithByte(42).WithRange(0, 10)
		if got := e.ExitCode(); got != c.want {
			t.Errorf("%s exit = %d, want %d", c.code, got, c.want)
		}
		if got := ExitCodeOf(e); got != c.want {
			t.Errorf("ExitCodeOf(%s) = %d, want %d", c.code, got, c.want)
		}
		if off, ok := ByteOffsetOf(e); !ok || off != 42 {
			t.Errorf("offset = %d/%v, want 42/true", off, ok)
		}
	}
	msg := E(CodeAnchorAmbiguous, "anchor", "two candidates").WithPath("x.mp4").WithByte(7).WithRange(0, 1).Error()
	for _, want := range []string{"anchor_ambiguous", "x.mp4", "offset=7", "time=[0,1)"} {
		if !contains(msg, want) {
			t.Errorf("error %q missing %q", msg, want)
		}
	}
}

func contains(s, sub string) bool {
	for i := 0; i+len(sub) <= len(s); i++ {
		if s[i:i+len(sub)] == sub {
			return true
		}
	}
	return false
}
