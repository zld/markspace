package clock

import "testing"

func TestNewTimelineRejectsZero(t *testing.T) {
	if _, err := NewTimeline(0); err != ErrInvalidTimescale {
		t.Fatalf("expected ErrInvalidTimescale, got %v", err)
	}
}

func TestToNanosExact(t *testing.T) {
	tl, err := NewTimeline(1000)
	if err != nil {
		t.Fatal(err)
	}
	cases := []struct {
		ticks uint64
		want  int64
	}{
		{0, 0},
		{1, 1_000_000},
		{2, 2_000_000},
		{1000, NanoPerSecond},
		{2500, 2_500_000_000},
	}
	for _, c := range cases {
		if got := tl.ToNanos(c.ticks); got != c.want {
			t.Errorf("ToNanos(%d) = %d, want %d", c.ticks, got, c.want)
		}
	}
}

func TestToNanosRounding(t *testing.T) {
	// timescale 3: one tick is 333333333.333... ns; halves round up.
	tl, _ := NewTimeline(3)
	if got := tl.ToNanos(1); got != 333_333_333 {
		t.Errorf("ToNanos(1) = %d, want 333333333", got)
	}
	if got := tl.ToNanos(2); got != 666_666_667 {
		t.Errorf("ToNanos(2) = %d, want 666666667", got)
	}
}

func TestTickNanos(t *testing.T) {
	low, _ := NewTimeline(90000)
	if got := low.TickNanos(); got != 11112 {
		t.Errorf("90kHz tick = %d ns, want 11112", got)
	}
	high, err := NewTimeline(1_000_000_000)
	if err != nil {
		t.Fatal(err)
	}
	if got := high.TickNanos(); got != 1 {
		t.Errorf("1GHz tick = %d ns, want 1", got)
	}
}

func TestWithinTolerance(t *testing.T) {
	if !WithinTolerance(100, 101, 1) {
		t.Error("difference of 1 should be within tolerance 1")
	}
	if WithinTolerance(100, 102, 1) {
		t.Error("difference of 2 should exceed tolerance 1")
	}
	if !WithinTolerance(5, -5, 10) {
		t.Error("absolute difference should be used")
	}
}
