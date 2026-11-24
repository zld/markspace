package media

import (
	"errors"
	"math"
	"testing"
)

func TestExtend33Wrap(t *testing.T) {
	// Walk forward across the 33-bit boundary: time must stay monotonic.
	prev := int64(Wrap33 - 10)
	got := Extend33(prev, 5)
	want := Wrap33 + 5
	if got != want {
		t.Fatalf("Extend33 wrap: got %d want %d", got, want)
	}
	if got <= prev {
		t.Fatal("extended value must remain monotonic across wrap")
	}
}

func TestExtend32RTPWrap(t *testing.T) {
	prev := int64(Wrap32 - 4)
	if got := Extend32(prev, 6); got != Wrap32+6 {
		t.Fatalf("Extend32 wrap: got %d", got)
	}
	// A genuine backwards jump must not be rewritten as a wrap.
	if got := Extend32(1000, 900); got != 900 {
		t.Fatalf("Extend32 backstep: got %d want 900", got)
	}
}

func TestUnwrapperMonotoneAndGuardrails(t *testing.T) {
	u := NewUnwrapper(33, 5, 1_000_000)
	v, err := u.Push(uint64(Wrap33 - 2))
	if err != nil || v != Wrap33-2 {
		t.Fatalf("first push: v=%d err=%v", v, err)
	}
	v, err = u.Push(3) // wraps
	if err != nil || v != Wrap32*2+3 { // 2^33 + 3
		t.Fatalf("wrap push: v=%d err=%v", v, err)
	}
	if _, err = u.Push(uint64(Wrap33 - 7)); err == nil || ClassOf(err) != ClassClock {
		t.Fatalf("backstep beyond tolerance must raise ClassClock, got %v", err)
	}
	if u.Prev() != Wrap32*2+3 {
		t.Fatal("failed push must not advance the unwrapper state")
	}
}

func TestClockConversions(t *testing.T) {
	if got := TicksToMillis(90); math.Abs(got-1.0) > 1e-9 {
		t.Fatalf("90 ticks = %v ms, want 1.0", got)
	}
	if got := SecondsToTicks(0.5); got != 45000 {
		t.Fatalf("0.5s = %d ticks, want 45000", got)
	}
	if got := PCRToTicks(27_000_000); got != 90_000 {
		t.Fatalf("PCR 27MHz -> ticks: %d", got)
	}
	c := NewClock(0, 10)
	if err := c.DriftError(100, 120); err == nil || ClassOf(err) != ClassClock {
		t.Fatalf("drift outside bound must fail, got %v", err)
	}
	if err := c.DriftError(100, 105); err != nil {
		t.Fatalf("drift within bound must pass, got %v", err)
	}
}

func TestMediaErrorClassification(t *testing.T) {
	me := New(ClassTruncated, "ts.parse", "packet cut short").
		WithPID(0x100).WithIndex(7).WithRange(1316, 1400)
	if me.Strategy != StrategyRecover {
		t.Fatalf("truncated strategy = %s, want recover", me.Strategy)
	}
	wrapped := Wrap(ClassIO, "journal", errors.New("disk full"), "append failed")
	if !errors.Is(wrapped, wrapped.Err) {
		t.Fatal("Unwrap chain broken")
	}
	if ClassOf(wrapped) != ClassIO || ClassOf(errors.New("plain")) != "" {
		t.Fatal("ClassOf extraction failed")
	}
	for _, c := range []Class{ClassClock, ClassValidation} {
		if DefaultStrategy(c) != StrategyRollback {
			t.Fatalf("class %s must roll back", c)
		}
	}
}
