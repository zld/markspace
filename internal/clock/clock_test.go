package clock

import (
	"testing"
	"time"
)

func TestFromTimescaleRounds(t *testing.T) {
	cases := []struct {
		value     int64
		timescale uint32
		want      Time
	}{
		{0, 90000, 0},
		{90000, 90000, Second},
		{1, 3, 333333333},
		{2, 3, 666666667},
		{48000, 48000, Second},
		{-1, 3, -333333333},
		{1 << 40, 1000, 1099511627776000000},
	}
	for _, c := range cases {
		got, err := FromTimescale(c.value, c.timescale)
		if err != nil {
			t.Fatalf("FromTimescale(%d,%d): %v", c.value, c.timescale, err)
		}
		if got != c.want {
			t.Errorf("FromTimescale(%d,%d)=%d want %d", c.value, c.timescale, got, c.want)
		}
	}
	if _, err := FromTimescale(1, 0); err == nil {
		t.Fatal("expected zero timescale error")
	}
}

func TestExtendSequenceNumberWrap(t *testing.T) {
	prev := uint64(0xFFF0)
	got := ExtendSequenceNumber(0x0003, prev)
	if got != 0x10003 {
		t.Fatalf("wrap extension got %d want %d", got, uint64(0x10003))
	}
	// A late packet five positions behind maps to the previous cycle.
	if got := ExtendSequenceNumber(0xFFFE, 0x10003); got != 0xFFFE {
		t.Fatalf("late extension got %d want %d", got, uint64(0xFFFE))
	}
	if got := ExtendSequenceNumber(0xFFFE, 2); got != 0 {
		t.Fatalf("underflow extension got %d want 0", got)
	}
}

func TestGuardMonotonic(t *testing.T) {
	var g Guard
	if got, ok := g.Admit(100); !ok || got != 100 {
		t.Fatal("first admit must pass")
	}
	if got, ok := g.Admit(90); ok || got != 100 {
		t.Fatalf("late time admitted: got=%d ok=%v", got, ok)
	}
	if got, ok := g.Admit(100); !ok || got != 100 {
		t.Fatal("equal time must pass")
	}
	if got, ok := g.Admit(101); !ok || got != 101 {
		t.Fatal("later time must pass")
	}
}

func TestFromMillisecondsRoundTrip(t *testing.T) {
	for _, ms := range []float64{0, 1, 33.333, 999.5} {
		if got := ToMilliseconds(FromMilliseconds(ms)); got != ms {
			t.Errorf("round trip %v -> %v", ms, got)
		}
	}
}

func TestFromRTPTimestampWrap(t *testing.T) {
	ext := ExtendRTPTimestamp(5, uint64(0xFFFFFFFA))
	var got Time
	var err error
	delta := ext - uint64(0xFFFFFFFA)
	got, err = FromRTPTimestamp(delta, 90000)
	if err != nil {
		t.Fatal(err)
	}
	// 11 ticks at 90 kHz is ~122.2 microseconds.
	if got <= 122*Microsecond || got >= 123*Microsecond {
		t.Fatalf("wrapped delta = %d", got)
	}
}

func TestTimeAdd(t *testing.T) {
	if got := Second.Add(1500 * time.Millisecond); got != 2500*Millisecond {
		t.Fatalf("Add = %d", got)
	}
}
