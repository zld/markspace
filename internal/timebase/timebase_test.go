package timebase

import (
	"math"
	"testing"
)

func TestBaseConvertExact(t *testing.T) {
	cases := []struct {
		base  Base
		ticks int64
		want  int64
	}{
		{Base{2, 1}, 1, 500_000_000}, // half a second
		{Base{90000, 1}, 90000, 1_000_000_000},
		{Base{90000, 1}, 3000, 33_333_333}, // rounds 33_333_333.33
		{Base{1000, 1}, 1500, 1_500_000_000},
		{Base{30000, 1001}, 30000, 1_001_000_000_000},
		{Base{48000, 1}, 1024, 21_333_333},
	}
	for _, c := range cases {
		got, err := c.base.ToNanos(c.ticks, 1, 0)
		if err != nil {
			t.Fatalf("%s ticks=%d: %v", c.base, c.ticks, err)
		}
		if got != c.want {
			t.Fatalf("%s ticks=%d: got %d want %d", c.base, c.ticks, got, c.want)
		}
	}
}

func TestBaseRoundTrip(t *testing.T) {
	base := Base{30000, 1001}
	for _, ticks := range []int64{0, 1, 1001, 30000, 90000, 1_000_000} {
		ns, err := base.ToNanos(ticks, 7, 12)
		if err != nil {
			t.Fatalf("ToNanos(%d): %v", ticks, err)
		}
		back, err := base.FromNanos(ns, 7, 12)
		if err != nil {
			t.Fatalf("FromNanos(%d): %v", ns, err)
		}
		if diff := back - ticks; diff > 1 || diff < -1 {
			t.Fatalf("round trip %d -> %d -> %d drifts by %d", ticks, ns, back, diff)
		}
	}
}

func TestBaseOverflow(t *testing.T) {
	base := Base{90000, 1}
	_, err := base.ToNanos(math.MaxInt64, 1, 0)
	if err == nil {
		t.Fatal("expected overflow error")
	}
	if err.Code != CodeTimeOverflow {
		t.Fatalf("code = %s, want %s", err.Code, CodeTimeOverflow)
	}
	if err.TrackID != 1 || err.Offset != 0 {
		t.Fatalf("error lacks track/offset: %+v", err)
	}
}

func TestNegativeTicks(t *testing.T) {
	base := Base{1000, 1}
	ns, err := base.ToNanos(-1500, 0, -1)
	if err != nil {
		t.Fatal(err)
	}
	if ns != -1_500_000_000 {
		t.Fatalf("got %d, want -1500000000", ns)
	}
	if _, err := base.NanoDuration(-1, 0, 0); err == nil || err.Code != CodeTimeRegression {
		t.Fatalf("negative duration code = %v", err)
	}
}

func TestParseRate(t *testing.T) {
	for _, c := range []struct {
		in   string
		num  int64
		den  int64
		fail bool
	}{
		{"90000", 90000, 1, false},
		{"30000/1001", 30000, 1001, false},
		{"0", 0, 1, true},
		{"-1/2", -1, 2, true},
		{"abc", 0, 0, true},
	} {
		b, err := ParseRate(c.in)
		if c.fail {
			if err == nil {
				t.Fatalf("%q: expected error", c.in)
			}
			continue
		}
		if err != nil {
			t.Fatalf("%q: %v", c.in, err)
		}
		if b.Num != c.num || b.Den != c.den {
			t.Fatalf("%q: got %s", c.in, b)
		}
	}
}

func TestRangeClamp(t *testing.T) {
	media := RangeNS{Start: 0, End: 1000}
	got := RangeNS{Start: -500, End: 1500}.Clamp(media)
	if got != media {
		t.Fatalf("clamp = %+v", got)
	}
	empty := RangeNS{Start: 2000, End: 3000}.Clamp(media)
	if empty.Valid() && empty.Start == 0 && empty.End == 1000 {
		t.Fatal("expected empty range outside media")
	}
	a := RangeNS{Start: 0, End: 10}
	if !a.Overlaps(RangeNS{Start: 9, End: 20}) {
		t.Fatal("expected overlap")
	}
	if a.Overlaps(RangeNS{Start: 10, End: 20}) {
		t.Fatal("half-open ranges must not overlap at the boundary")
	}
}

func TestErrorFormatting(t *testing.T) {
	e := Newf(CodeEventOutOfRange, 3, 99, "event %q beyond media", "cue-1")
	got := e.Error()
	want := `EVENT_OUT_OF_RANGE track=3 offset=99: event "cue-1" beyond media`
	if got != want {
		t.Fatalf("got %q want %q", got, want)
	}
	if !HasCode(e, CodeEventOutOfRange) {
		t.Fatal("HasCode missed the code")
	}
	if HasCode(nil, CodeInternal) {
		t.Fatal("nil error must not match")
	}
	wrapped := AsError(e).WithTrack(9).WithOffset(1)
	if wrapped.TrackID != 9 || wrapped.Offset != 1 || wrapped.Code != CodeEventOutOfRange {
		t.Fatalf("wrap lost fields: %+v", wrapped)
	}
}
