package media

import (
	"errors"
	"testing"
)

func TestNewTimeBaseRejectsZero(t *testing.T) {
	if _, err := NewTimeBase(0); err == nil {
		t.Fatal("expected error for zero time base")
	}
	tb, err := NewTimeBase(48000)
	if err != nil || !tb.Valid() || tb.String() != "48000Hz" {
		t.Fatalf("unexpected time base: %v %v", tb, err)
	}
}

func TestDurationAddSameClock(t *testing.T) {
	tb := TimeBase{TicksPerSecond: 1000}
	a := Duration{Ticks: 1500, Base: tb}
	b := Duration{Ticks: 500, Base: tb}
	sum, err := a.Add(b)
	if err != nil || sum.Ticks != 2000 || sum.Milliseconds() != 2000 {
		t.Fatalf("add failed: %+v err=%v", sum, err)
	}
	other := Duration{Ticks: 1, Base: TimeBase{TicksPerSecond: 90000}}
	if _, err := a.Add(other); err == nil {
		t.Fatal("expected clock-mismatch error")
	}
}

func TestDurationConvertTo(t *testing.T) {
	// 48 kHz, 48000 ticks == 1 second -> 44100 ticks on the AAC clock.
	src := Duration{Ticks: 48000, Base: TimeBase{TicksPerSecond: 48000}}
	dst, err := src.ConvertTo(TimeBase{TicksPerSecond: 44100})
	if err != nil {
		t.Fatal(err)
	}
	if dst.Ticks != 44100 {
		t.Fatalf("expected 44100 ticks, got %d", dst.Ticks)
	}
	// 1000 ticks at 1000Hz = 1s -> 2000 ticks at 2000Hz.
	one := Duration{Ticks: 1000, Base: TimeBase{TicksPerSecond: 1000}}
	got, err := one.ConvertTo(TimeBase{TicksPerSecond: 2000})
	if err != nil || got.Ticks != 2000 {
		t.Fatalf("conversion: %d err=%v", got.Ticks, err)
	}
}

func TestConvertOverflow(t *testing.T) {
	big := Duration{Ticks: 1 << 62, Base: TimeBase{TicksPerSecond: 1000}}
	if _, err := big.ConvertTo(TimeBase{TicksPerSecond: 48000}); err == nil {
		t.Fatal("expected overflow error")
	}
}

func TestCommonClock(t *testing.T) {
	tb, err := CommonClock(TimeBase{TicksPerSecond: 48000}, TimeBase{TicksPerSecond: 44100})
	if err != nil {
		t.Fatal(err)
	}
	// lcm(48000,44100) = 7,056,000
	if tb.TicksPerSecond != 7056000 {
		t.Fatalf("unexpected common clock: %d", tb.TicksPerSecond)
	}
}

func TestErrorModel(t *testing.T) {
	err := NewError(KindCorrupt, StageParseRTP, "bad header", errors.New("x")).
		WithTrack("cam1").WithSequence(42).AtOffset(137)
	me, ok := AsError(err)
	if !ok || me.Offset != 137 || me.Track != "cam1" || me.Sequence != 42 {
		t.Fatalf("error fields lost: %+v", me)
	}
	if me.Error() == "" || me.Unwrap() == nil {
		t.Fatal("error message or cause missing")
	}
	if _, ok := AsError(errors.New("plain")); ok {
		t.Fatal("plain error must not be classified as media.Error")
	}
}
