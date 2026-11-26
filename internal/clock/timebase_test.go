package clock

import "testing"

func TestExpandWrapsAt2to33(t *testing.T) {
	c := New()
	nearTop := int64(PCRBaseModulus-10) * 300
	ticks, wrapped, err := c.Expand(nearTop)
	if err != nil || wrapped || ticks != nearTop {
		t.Fatalf("first expand: ticks=%d wrapped=%v err=%v", ticks, wrapped, err)
	}
	// After the wrap the raw value is small again; unified time must keep
	// increasing by exactly one modulus plus the small delta.
	small := int64(5) * 300
	ticks2, wrapped, err := c.Expand(small)
	if err != nil || !wrapped || c.Wraps() != 1 {
		t.Fatalf("wrap expand: wrapped=%v err=%v wraps=%d", wrapped, err, c.Wraps())
	}
	want := int64(nearTop) + (PCRModulus - nearTop) + small
	if ticks2 != want {
		t.Fatalf("unified ticks across wrap: got %d want %d", ticks2, want)
	}
	if ticks2 <= ticks {
		t.Fatalf("unified time moved backwards: %d -> %d", ticks, ticks2)
	}
}

func TestExpandSmallBackwardJumpHoldsTime(t *testing.T) {
	c := New()
	first, _, _ := c.Expand(1_000_000)
	held, wrapped, err := c.Expand(999_000)
	if err != ErrBackwards || wrapped {
		t.Fatalf("expected ErrBackwards, got err=%v wrapped=%v", err, wrapped)
	}
	if held != first {
		t.Fatalf("time must be held, not fabricated: got %d want %d", held, first)
	}
}

func TestExpandRejectsOutOfRangePCR(t *testing.T) {
	c := New()
	if _, _, err := c.Expand(-1); err == nil {
		t.Fatal("negative PCR accepted")
	}
	if _, _, err := c.Expand(PCRModulus); err == nil {
		t.Fatal("PCR at modulus accepted")
	}
}

func TestTimelineNeverInterpolates(t *testing.T) {
	var tl Timeline
	if _, ok := tl.At(10); ok {
		t.Fatal("empty timeline produced an anchor")
	}
	tl.Update(100, 27_000_000)
	a, ok := tl.At(150)
	if !ok || a.Ticks != 27_000_000 || a.Offset != 100 {
		t.Fatalf("stale anchor wrong: %+v ok=%v", a, ok)
	}
	if _, ok := tl.At(50); ok {
		t.Fatal("anchor returned for an earlier offset")
	}
}

func TestConversions(t *testing.T) {
	if got := Seconds(TicksPerSecond * 2); got != 2.0 {
		t.Fatalf("seconds: got %v", got)
	}
	if got := Nanoseconds(TicksPerSecond); got != 1_000_000_000 {
		t.Fatalf("nanoseconds: got %d", got)
	}
}
