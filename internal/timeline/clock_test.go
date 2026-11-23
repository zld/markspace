package timeline

import (
	"errors"
	"math"
	"testing"
)

func TestWrapClock16Rollover(t *testing.T) {
	w := NewWrapClock(16)
	cases := []struct {
		raw  uint64
		want uint64
	}{
		{65534, 65534},
		{65535, 65535},
		{0, 65536},
		{1, 65537},
		{100, 65636},
	}
	for _, c := range cases {
		if got := w.Unwrap(c.raw); got != c.want {
			t.Fatalf("Unwrap(%d)=%d want %d", c.raw, got, c.want)
		}
	}
}

func TestWrapClock32Rollover(t *testing.T) {
	w := NewWrapClock(32)
	base := uint64(math.MaxUint32) - 2
	for i := uint64(0); i < 6; i++ {
		raw := (base + i) % (1 << 32)
		got := w.Unwrap(raw)
		want := base + i
		if got != want {
			t.Fatalf("step %d Unwrap(%d)=%d want %d", i, raw, got, want)
		}
	}
}

func TestWrapClockReorderDoesNotRegress(t *testing.T) {
	w := NewWrapClock(16)
	w.Unwrap(1000)
	w.Unwrap(1005)
	// Late packet with a lower raw sequence number.
	if got := w.Unwrap(1002); got != 1005 {
		t.Fatalf("reordered unwrap=%d want 1005", got)
	}
}

func TestTimeBaseRoundTrip(t *testing.T) {
	tb := TimeBase{ClockRate: 90000}
	ns, err := tb.ToNanos(90000) // exactly one second
	if err != nil || ns != 1_000_000_000 {
		t.Fatalf("ToNanos one second = %d, %v", ns, err)
	}
	ticks, err := tb.NanosToTicks(ns)
	if err != nil || ticks != 90000 {
		t.Fatalf("NanosToTicks = %d, %v", ticks, err)
	}
}

func TestTimeBaseMatroskaMillis(t *testing.T) {
	tb := TimeBase{ClockRate: 1000}
	ns, err := tb.ToNanos(500)
	if err != nil || ns != 500_000_000 {
		t.Fatalf("ToNanos(500ms)=%d, %v", ns, err)
	}
}

func TestToNanosOverflow(t *testing.T) {
	tb := TimeBase{ClockRate: 1}
	_, err := tb.ToNanos(math.MaxUint64 / 2)
	if !errors.Is(err, ErrOverflow) {
		t.Fatalf("expected ErrOverflow, got %v", err)
	}
}

func TestNanosToTicksRejectsNegative(t *testing.T) {
	tb := TimeBase{ClockRate: 1000}
	if _, err := tb.NanosToTicks(-1); !errors.Is(err, ErrOverflow) {
		t.Fatalf("expected ErrOverflow, got %v", err)
	}
	if _, err := (TimeBase{}).ToNanos(1); err == nil {
		t.Fatal("zero clock rate must error")
	}
}

func TestAdvance(t *testing.T) {
	v, err := Advance(1000, -250)
	if err != nil || v != 750 {
		t.Fatalf("Advance = %d, %v", v, err)
	}
	if _, err := Advance(math.MaxInt64, 1); !errors.Is(err, ErrOverflow) {
		t.Fatalf("maxint advance expected overflow, got %v", err)
	}
	if _, err := Advance(math.MinInt64, -1); !errors.Is(err, ErrOverflow) {
		t.Fatalf("minint advance expected overflow, got %v", err)
	}
}

func TestCheckMonotonic(t *testing.T) {
	if err := CheckMonotonic(10, 10, 1); err != nil {
		t.Fatalf("equal timestamps must be allowed: %v", err)
	}
	if err := CheckMonotonic(11, 10, 1); !errors.Is(err, ErrOutOfBounds) {
		t.Fatalf("regression expected ErrOutOfBounds, got %v", err)
	}
}

func TestStructuredErrorUnwrap(t *testing.T) {
	e := NewError("timeline", "op", 42, RecoverPartial, "bad", ErrTruncated)
	if !errors.Is(e, ErrTruncated) {
		t.Fatal("errors.Is must reach wrapped sentinel")
	}
	if e.Offset != 42 || e.Level != RecoverPartial {
		t.Fatal("structured fields lost")
	}
	if s := RecoverNone.String(); s != "none" {
		t.Fatalf("level string = %q", s)
	}
}
