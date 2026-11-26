package model

import "testing"

func TestMulDivExact(t *testing.T) {
	got, err := MulDiv(3000, 1000, 90000)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got != 33 {
		t.Fatalf("MulDiv(3000,1000,90000) = %d, want 33", got)
	}
}

func TestMulDivOverflow(t *testing.T) {
	if _, err := MulDiv(1<<62, 1<<62, 1); err == nil || err.Code != ErrOverflow {
		t.Fatalf("want ErrOverflow, got %v", err)
	}
}

func TestMulDivZeroDivisor(t *testing.T) {
	if _, err := MulDiv(1, 1, 0); err == nil {
		t.Fatal("want error for zero divisor")
	}
}

func TestConvert(t *testing.T) {
	tm := Time{Ticks: 90000, Scale: 90000}
	got, err := tm.Convert(1000)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got.Ticks != 1000 || got.Scale != 1000 {
		t.Fatalf("got %+v, want {1000 1000}", got)
	}
}

func TestCmp(t *testing.T) {
	a := Time{Ticks: 1, Scale: 2}
	b := Time{Ticks: 2, Scale: 4}
	if a.Cmp(b) != 0 {
		t.Fatal("1/2 should equal 2/4")
	}
	if (Time{Ticks: 1, Scale: 3}).Cmp(a) >= 0 {
		t.Fatal("1/3 should be less than 1/2")
	}
}

func TestErrorString(t *testing.T) {
	e := &Error{Code: ErrTruncated, Track: 1, Offset: 41, Msg: "cut"}
	want := "TRUNCATED track=1 offset=41: cut"
	if e.Error() != want {
		t.Fatalf("got %q, want %q", e.Error(), want)
	}
}
