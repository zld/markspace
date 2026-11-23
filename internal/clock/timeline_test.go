package clock

import (
	"errors"
	"math"
	"testing"
)

func mustTimeline(t *testing.T, scale uint32) *Timeline {
	t.Helper()
	tl, err := NewTimeline(scale)
	if err != nil {
		t.Fatalf("timeline: %v", err)
	}
	return tl
}

func TestTickConversions(t *testing.T) {
	tl := mustTimeline(t, 90000)
	if got := tl.TickAtNS(1_000_000_000); got != 90000 {
		t.Fatalf("TickAtNS = %d", got)
	}
	if got := tl.NSAtTick(45000); got != 500_000_000 {
		t.Fatalf("NSAtTick = %d", got)
	}
	if got := tl.Rebucket(90000, 48000); got != 48000 {
		t.Fatalf("Rebucket = %d", got)
	}
	if _, err := NewTimeline(0); err == nil {
		t.Fatal("zero timescale accepted")
	}
}

func TestUnwrapperForwardAndBackwardWrap(t *testing.T) {
	var u Unwrapper
	first, err := u.Extend(0xFFFFFF00)
	if err != nil {
		t.Fatal(err)
	}
	next, err := u.Extend(0x00000100)
	if err != nil {
		t.Fatal(err)
	}
	if next-first != 512 {
		t.Fatalf("forward wrap delta = %d want 512", next-first)
	}

	var b Unwrapper
	if _, err := b.Extend(0x00000100); err != nil {
		t.Fatal(err)
	}
	back, err := b.Extend(0xFFFFFF00)
	if err != nil {
		t.Fatal(err)
	}
	if back != -256 {
		t.Fatalf("backward wrap = %d want -256", back)
	}
}

func TestUnwrapperDuplicateIsStable(t *testing.T) {
	var u Unwrapper
	a, _ := u.Extend(1234)
	b, _ := u.Extend(1234)
	if a != b {
		t.Fatalf("duplicate timestamp moved: %d != %d", a, b)
	}
}

func TestRateScheduleTransitions(t *testing.T) {
	tl := mustTimeline(t, 90000)
	fps, _ := NewRational(30000, 1001)
	s, err := NewRateSchedule(tl, fps)
	if err != nil {
		t.Fatal(err)
	}
	if err := s.AddTransition(30, Rational{Num: 25, Den: 1}); err != nil {
		t.Fatal(err)
	}
	if got := s.TickAtFrame(0); got != 0 {
		t.Fatalf("frame 0 = %d", got)
	}
	// 30 frames at 29.97 fps = 1001 ms -> 90090 ticks.
	if got := s.TickAtFrame(30); got != 90090 {
		t.Fatalf("frame 30 = %d want 90090", got)
	}
	// Next 25 frames at 25 fps = 1 second -> 180090 ticks.
	if got := s.TickAtFrame(55); got != 180090 {
		t.Fatalf("frame 55 = %d want 180090", got)
	}
	if got := s.FrameDuration(30); got != 3600 {
		t.Fatalf("frame duration = %d want 3600", got)
	}
	if err := s.AddTransition(30, Rational{Num: 24, Den: 1}); err == nil {
		t.Fatal("non-advancing transition accepted")
	}
}

func TestConverterOffsetAndWrap(t *testing.T) {
	tl := mustTimeline(t, 90000)
	c := &Converter{Timeline: tl, ClockRate: 48000, Offset: 1500}
	if _, err := c.Prime(0xFFFFFF00); err != nil {
		t.Fatal(err)
	}
	// Same instant.
	tick, err := c.LocalTick(0xFFFFFF00)
	if err != nil {
		t.Fatal(err)
	}
	if tick != 1500 {
		t.Fatalf("anchor tick = %d", tick)
	}
	// 1600 samples later (1200 ticks at 90k) crossing wrap.
	tick, err = c.LocalTick(0x00000100)
	if err != nil {
		t.Fatal(err)
	}
	if tick != 1500+960 {
		t.Fatalf("cross-wrap tick = %d want 2460", tick)
	}
	if !WithinTolerance(tick, 2360, 100) {
		t.Fatal("tolerance rejected 100 tick delta")
	}
	if WithinTolerance(tick, 2360, 99) {
		t.Fatal("tolerance accepted 100 tick delta")
	}
}

func TestMulDivExact(t *testing.T) {
	if got := mulDivRound(1600, 90000, 48000); got != 3000 {
		t.Fatalf("mulDivRound = %d", got)
	}
	if got := mulDivRound(-1600, 90000, 48000); got != -3000 {
		t.Fatalf("mulDivRound negative = %d", got)
	}
	if got := mulDivRound(1, 1, 3); got != 0 {
		t.Fatalf("mulDivRound third = %d", got)
	}
	if got := mulDivRound(2, 1, 3); got != 1 {
		t.Fatalf("mulDivRound two thirds = %d", got)
	}
}

func TestUnwrapperRejectsOverflow(t *testing.T) {
	var u Unwrapper
	u.started = true
	u.last = 0
	u.cycles = math.MaxInt64 - 5
	_, err := u.Extend(10)
	if !errors.Is(err, ErrWrapAmbiguous) {
		t.Fatalf("expected wrap ambiguity, got %v", err)
	}
}
