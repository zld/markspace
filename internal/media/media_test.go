package media

import (
	"errors"
	"testing"
)

func TestTimebaseRoundTrip(t *testing.T) {
	cases := []struct {
		ts    uint32
		ticks uint64
		ns    int64
	}{
		{48000, 0, 0},
		{48000, 480, 10_000_000},
		{90000, 900, 10_000_000},
		{90000, 1, 11111}, // truncated toward zero
		{1000, 1, 1_000_000},
		{44100, 441, 10_000_000},
	}
	for _, c := range cases {
		tb := Timebase{Track: "audio", Timescale: c.ts}
		ns, err := tb.ToNs(c.ticks)
		if err != nil {
			t.Fatalf("%s:%d: %v", tb, c.ticks, err)
		}
		if ns != c.ns {
			t.Fatalf("%s ticks=%d: got %dns want %dns", tb, c.ticks, ns, c.ns)
		}
	}
}

func TestTimebaseOrigin(t *testing.T) {
	tb := Timebase{Track: "audio", Timescale: 48000, OriginTicks: 4800}
	ns, err := tb.ToNs(0)
	if err != nil {
		t.Fatal(err)
	}
	if ns != 100_000_000 {
		t.Fatalf("origin: got %dns want 100ms", ns)
	}
	ticks, err := tb.FromNs(ns)
	if err != nil {
		t.Fatal(err)
	}
	if ticks != 0 {
		t.Fatalf("origin roundtrip: got %d want 0", ticks)
	}
	if _, err := tb.FromNs(ns - 1); err == nil {
		t.Fatal("expected error for time before track origin")
	} else if KindOf(err) != KindTimebase {
		t.Fatalf("kind = %s, want timebase", KindOf(err))
	}
}

func TestTimebaseZeroTimescale(t *testing.T) {
	tb := Timebase{Track: "audio", Timescale: 0}
	_, err := tb.ToNs(1)
	if err == nil {
		t.Fatal("expected zero-timescale error")
	}
	var me *Error
	if !errors.As(err, &me) {
		t.Fatalf("not a structured error: %v", err)
	}
	if me.Loc.Track != "audio" {
		t.Fatalf("track = %q, want audio", me.Loc.Track)
	}
	if KindOf(err) != KindTimebase {
		t.Fatalf("kind = %s", KindOf(err))
	}
}

func TestTimebaseOverflow(t *testing.T) {
	// ticks*1e9 must not wrap silently.
	tb := Timebase{Track: "audio", Timescale: 1}
	if _, err := tb.ToNs(1 << 62); err == nil {
		t.Fatal("expected overflow error")
	} else if KindOf(err) != KindTimebase {
		t.Fatalf("kind = %s, want timebase", KindOf(err))
	}
	tb2 := Timebase{Track: "audio", Timescale: 48000, OriginTicks: 1 << 63}
	if _, err := tb2.ToNs(1 << 63); err == nil {
		t.Fatal("expected origin overflow error")
	}
}

func TestScaledExactAndFractional(t *testing.T) {
	a := Timebase{Track: "audio", Timescale: 48000}
	v := Timebase{Track: "video", Timescale: 90000}
	got, err := Scaled(480, a, v)
	if err != nil {
		t.Fatal(err)
	}
	if got != 900 {
		t.Fatalf("scaled = %d, want 900", got)
	}
	// 1 tick at 48k is 1.875 ticks at 90k: not representable.
	if _, err := Scaled(1, a, v); err == nil {
		t.Fatal("expected fractional-tick error")
	} else if KindOf(err) != KindTimebase {
		t.Fatalf("kind = %s", KindOf(err))
	}
	if _, err := NewRatio(1, 0); err == nil {
		t.Fatal("expected zero-denominator error")
	}
}

func TestTrackListOrder(t *testing.T) {
	tl := NewTrackList()
	for _, tb := range []Timebase{
		{Track: "video", Timescale: 90000},
		{Track: "audio", Timescale: 48000},
		{Track: "subtitle", Timescale: 1000},
	} {
		if err := tl.Put(tb); err != nil {
			t.Fatal(err)
		}
	}
	all := tl.All()
	if len(all) != 3 || all[0].Track != "audio" || all[1].Track != "subtitle" || all[2].Track != "video" {
		t.Fatalf("unexpected order: %v", all)
	}
	if err := tl.Put(Timebase{Track: "x", Timescale: 0}); err == nil {
		t.Fatal("expected validation error")
	}
}

func TestErrorLocationPresence(t *testing.T) {
	e0 := At(Newf(KindInput, "bad box"), Loc{Track: "subtitle", Offset: 0, HasOffset: true})
	if !e0.Loc.HasOffset || e0.Loc.Offset != 0 {
		t.Fatalf("offset 0 must be preserved as recorded: %+v", e0.Loc)
	}
	if e0.Error() != "input: bad box (track=subtitle offset=0)" {
		t.Fatalf("error text = %q", e0.Error())
	}
	inner := At(Newf(KindInput, "bad box"), Loc{Track: "video", Sample: 7})
	outer := At(inner, Loc{Track: "audio", Sample: 1, Stage: "parse"})
	if outer.Loc.Track != "video" || outer.Loc.Sample != 7 || outer.Loc.Stage != "parse" {
		t.Fatalf("inner location must win: %+v", outer.Loc)
	}
	if got := KindOf(errors.New("plain")); got != KindInput {
		t.Fatalf("foreign error kind = %s", got)
	}
	if _, ok := LocOf(errors.New("plain")); ok {
		t.Fatal("foreign error should carry no structured location")
	}
}
