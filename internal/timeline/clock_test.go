package timeline

import (
	"math"
	"testing"
)

func TestMsToTicksRoundTrip(t *testing.T) {
	ticks, err := MsToTicks(1000)
	if err != nil {
		t.Fatal(err)
	}
	if ticks != Ticks(90000) {
		t.Fatalf("1000ms = %d ticks, want 90000 (90 kHz)", ticks)
	}
	if got := ticks.Millis(); got != 1000 {
		t.Fatalf("roundtrip = %dms, want 1000", got)
	}
	small, err := MsToTicks(15)
	if err != nil || small != 1350 || small.MillisFloat() != 15 {
		t.Fatalf("15ms = %d ticks / %.4f ms", small, small.MillisFloat())
	}
}

func TestMsToTicksOverflow(t *testing.T) {
	if _, err := MsToTicks(math.MaxInt64); err != ErrOverflow {
		t.Fatalf("want ErrOverflow, got %v", err)
	}
	if _, err := MsToTicks(math.MinInt64); err != ErrOverflow {
		t.Fatalf("want ErrOverflow, got %v", err)
	}
}

func TestFrameDuration(t *testing.T) {
	d, err := FrameDuration(25, 1)
	if err != nil {
		t.Fatal(err)
	}
	if d != 3600 {
		t.Fatalf("25fps frame = %d ticks, want 3600", d)
	}
	d, err = FrameDuration(30000, 1001)
	if err != nil {
		t.Fatal(err)
	}
	if d != 3003 {
		t.Fatalf("29.97fps frame = %d ticks, want 3003", d)
	}
	if _, err := FrameDuration(0, 1); err == nil {
		t.Fatal("want error for zero frame rate")
	}
}

func TestAddSafeOverflow(t *testing.T) {
	if _, err := AddSafe(Ticks(math.MaxInt64-10), Ticks(100)); err != ErrOverflow {
		t.Fatalf("want ErrOverflow, got %v", err)
	}
	v, err := AddSafe(Ticks(100), Ticks(50))
	if err != nil || v != 150 {
		t.Fatalf("AddSafe(100,50) = %d, %v", v, err)
	}
}

func TestAbsDiff(t *testing.T) {
	if d := AbsDiff(Ticks(math.MinInt64+1), Ticks(math.MaxInt64)); d <= 0 {
		t.Fatalf("AbsDiff overflowed: %d", d)
	}
	if d := AbsDiff(10, 40); d != 30 {
		t.Fatalf("AbsDiff(10,40) = %d", d)
	}
}
