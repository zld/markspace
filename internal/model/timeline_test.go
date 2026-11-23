package model

import (
	"encoding/json"
	"errors"
	"strings"
	"testing"
)

func TestNewTimeNormalizesAndCarries(t *testing.T) {
	cases := []struct {
		ticks int64
		num   int64
		den   int64
		want  Time
	}{
		{0, 0, 1, Time{Ticks: 0, Rem: Rational{Den: 1}}},
		{5, 1, 2, Time{Ticks: 5, Rem: Rational{Num: 1, Den: 2}}},
		{0, 3, 2, Time{Ticks: 1, Rem: Rational{Num: 1, Den: 2}}},
		{0, -1, 2, Time{Ticks: -1, Rem: Rational{Num: 1, Den: 2}}},
		{-2, -3, 2, Time{Ticks: -4, Rem: Rational{Num: 1, Den: 2}}},
	}
	for _, c := range cases {
		got, err := NewTime(c.ticks, Rational{Num: c.num, Den: c.den})
		if err != nil {
			t.Fatalf("NewTime(%d,%d/%d): %v", c.ticks, c.num, c.den, err)
		}
		if got.Cmp(c.want) != 0 || got.Ticks != c.want.Ticks || got.Rem != c.want.Rem {
			t.Fatalf("NewTime(%d,%d/%d)=%+v want %+v", c.ticks, c.num, c.den, got, c.want)
		}
	}
}

func TestConvertTicksKeepsRemainder(t *testing.T) {
	// 101 ticks at 3/s expressed at 10/s is 336 + 2/3 ticks.
	got, err := ConvertTicks(101, 3, 10)
	if err != nil {
		t.Fatal(err)
	}
	if got.Ticks != 336 || got.Rem != (Rational{Num: 2, Den: 3}) {
		t.Fatalf("got %+v", got)
	}
	// The remainder survives repeated accumulation instead of being dropped.
	acc := TimeOfTicks(0)
	for i := 0; i < 3; i++ {
		acc, err = acc.Add(got)
		if err != nil {
			t.Fatal(err)
		}
	}
	want, _ := NewTime(1010, Rational{})
	if acc.Cmp(want) != 0 {
		t.Fatalf("accumulated %s want %s", acc, want)
	}
}

func TestConvertExactRejectsInexact(t *testing.T) {
	if _, err := ConvertExact(101, 3, 10); !errors.Is(err, ErrInexact) {
		t.Fatalf("want ErrInexact, got %v", err)
	}
	v, err := ConvertExact(90, 3, 10)
	if err != nil || v != 300 {
		t.Fatalf("got %d, %v", v, err)
	}
}

func TestSecondsToTime(t *testing.T) {
	// 6.006 s at 90000/s = 540540 ticks exactly.
	got, err := SecondsToTime(6006, 1000, 90000)
	if err != nil {
		t.Fatal(err)
	}
	if got.Ticks != 540540 || got.Rem.Num != 0 {
		t.Fatalf("got %+v", got)
	}
	// 1/3 s at 1000/s = 333 + 1/3 ticks.
	got, err = SecondsToTime(1, 3, 1000)
	if err != nil {
		t.Fatal(err)
	}
	if got.Ticks != 333 || got.Rem != (Rational{Num: 1, Den: 3}) {
		t.Fatalf("got %+v", got)
	}
}

func TestMillisecondsExact(t *testing.T) {
	tm := TimeOfTicks(540540)
	ms, rem, err := tm.Milliseconds(90000)
	if err != nil {
		t.Fatal(err)
	}
	if ms != 6006 || !rem.IsZero() {
		t.Fatalf("got %d %s", ms, rem)
	}
	tm = TimeOfTicks(1)
	ms, rem, err = tm.Milliseconds(3)
	if err != nil {
		t.Fatal(err)
	}
	if ms != 333 || rem != (Rational{Num: 1, Den: 3}) {
		t.Fatalf("got %d %s", ms, rem)
	}
}

func TestLCMAndSharedTimebase(t *testing.T) {
	base, err := LCMAll([]int64{90000, 48000, 30000})
	if err != nil {
		t.Fatal(err)
	}
	if base != 720000 {
		t.Fatalf("base=%d", base)
	}
	if err := CheckSharedTimebase(base, []int64{90000, 48000, 30000}); err != nil {
		t.Fatal(err)
	}
	if err := CheckSharedTimebase(90000, []int64{90000, 44100}); !errors.Is(err, ErrInexact) {
		t.Fatalf("want inexact, got %v", err)
	}
	if _, err := LCMAll([]int64{2000003, 2000039}); !errors.Is(err, ErrTimebaseTooLarge) {
		t.Fatalf("want too large, got %v", err)
	}
}

func TestTimeJSONRoundTrip(t *testing.T) {
	tm, _ := NewTime(12, Rational{Num: 5, Den: 7})
	b, err := json.Marshal(tm)
	if err != nil {
		t.Fatal(err)
	}
	if string(b) != `{"ticks":12,"rem":{"num":5,"den":7}}` {
		t.Fatalf("unexpected json %s", b)
	}
	var back Time
	if err := json.Unmarshal(b, &back); err != nil {
		t.Fatal(err)
	}
	if back.Cmp(tm) != 0 || back.Rem != tm.Rem {
		t.Fatalf("round trip %+v", back)
	}
}

func TestTimeCmpAndBounds(t *testing.T) {
	a, _ := NewTime(1, Rational{Num: 1, Den: 3})
	b, _ := NewTime(1, Rational{Num: 1, Den: 2})
	if a.Cmp(b) >= 0 {
		t.Fatalf("a should be below b")
	}
	if !strings.Contains(a.String(), "1/3") {
		t.Fatalf("string %q", a.String())
	}
	if _, err := NewTime(MaxTicks, Rational{Num: 3, Den: 2}); !errors.Is(err, ErrTimeOverflow) {
		t.Fatalf("want overflow, got %v", err)
	}
}

func TestFaultErrorIncludesLocation(t *testing.T) {
	f := NewFault(CodeDamageGap, SeverityError, Source{Layer: "inspect", Segment: "seg-3", File: "v1/3.m4s"}, "gap of %d ticks", 3000).
		WithMedia(9000, 12000, 90000)
	msg := f.Error()
	for _, want := range []string{"damage_gap", "segment=seg-3", "file=v1/3.m4s", "media=[9000,12000)/90000"} {
		if !strings.Contains(msg, want) {
			t.Fatalf("fault message %q missing %q", msg, want)
		}
	}
	if _, ok := AsFault(f); !ok {
		t.Fatal("AsFault should match")
	}
}

func TestFaultListDeterministic(t *testing.T) {
	var l FaultList
	l.Add(NewFault(CodeDamageGap, SeverityError, Source{Layer: "inspect", Segment: "b"}, "x"))
	l.Add(NewFault(CodeTransportGap, SeverityWarn, Source{Layer: "transport", Segment: "a"}, "y"))
	l.Add(NewFault(CodeDamageGap, SeverityError, Source{Layer: "inspect", Segment: "b"}, "x"))
	got := l.All()
	if len(got) != 2 {
		t.Fatalf("len=%d", len(got))
	}
	if got[0].Source.Layer != "inspect" || got[1].Source.Layer != "transport" {
		t.Fatalf("order %+v", got)
	}
	if f := l.First(); f == nil || f.Code != CodeDamageGap {
		t.Fatalf("first %+v", f)
	}
}

func TestFrameDurationMedian(t *testing.T) {
	d := []Time{TimeOfTicks(3000), TimeOfTicks(1000), TimeOfTicks(2000)}
	if got := FrameDuration(d); got.Ticks != 2000 {
		t.Fatalf("got %s", got)
	}
	if got := FrameDuration(nil); !got.IsZero() {
		t.Fatalf("empty %s", got)
	}
}
