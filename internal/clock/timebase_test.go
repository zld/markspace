package clock

import (
	"testing"

	"mediapack/internal/fault"
)

func TestTimebaseConversion(t *testing.T) {
	tb, err := New(1000, 0) // millisecond ticks
	if err != nil {
		t.Fatal(err)
	}
	cases := []struct {
		ticks int64
		nanos int64
	}{
		{0, 0},
		{1, 1_000_000},
		{250, 250_000_000},
		{1000, 1_000_000_000},
	}
	for _, c := range cases {
		if got := tb.ToNanos(c.ticks); got != c.nanos {
			t.Errorf("ToNanos(%d)=%d want %d", c.ticks, got, c.nanos)
		}
		if got := tb.FromNanos(c.nanos); got != c.ticks {
			t.Errorf("FromNanos(%d)=%d want %d", c.nanos, got, c.ticks)
		}
	}
}

func TestOffsetAndOddRate(t *testing.T) {
	tb, _ := New(48000, 500) // 48 kHz audio ticks, 500 ns offset
	if got := tb.ToNanos(48); got != 1_000_000+500 {
		t.Fatalf("offset/rate conversion wrong: %d", got)
	}
}

func TestZeroTickRateRejected(t *testing.T) {
	if _, err := New(0, 0); !fault.Is(err, fault.KindTimestampRegression) {
		t.Fatalf("expected structured fault, got %v", err)
	}
}

func TestMonitorAcceptsMonotonic(t *testing.T) {
	tb, _ := New(1000, 0)
	m := NewMonitor(1, tb)
	want := []int64{0, 1_000_000, 2_000_000, 2_000_000} // equal is allowed
	for i, ticks := range []int64{0, 1, 2, 2} {
		ns, err := m.Check(ticks, int64(100+i))
		if err != nil || ns != want[i] {
			t.Fatalf("case %d: ns=%d err=%v", i, ns, err)
		}
	}
}

func TestMonitorRejectsRegression(t *testing.T) {
	tb, _ := New(1000, 0)
	m := NewMonitor(2, tb)
	if _, err := m.Check(10, 10); err != nil {
		t.Fatal(err)
	}
	_, err := m.Check(9, 77)
	fe := fault.Of(err)
	if fe == nil || fe.Kind != fault.KindTimestampRegression || fe.Offset != 77 || fe.Track != 2 {
		t.Fatalf("expected regression fault at offset 77 track 2, got %v", err)
	}
	if !fe.Recoverable {
		t.Fatal("regression should be recoverable from checkpoint")
	}
}

func TestMonitorRejectsNegative(t *testing.T) {
	tb, _ := New(1000, 0)
	m := NewMonitor(1, tb)
	if _, err := m.Check(-5, 3); !fault.Is(err, fault.KindTimestampRegression) {
		t.Fatalf("expected negative-timestamp fault, got %v", err)
	}
}
