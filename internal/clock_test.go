package kernel

import "testing"

func TestClockUnwrapsSinglePCRWrap(t *testing.T) {
	c := NewClock(0x100)
	start := PCRBasePeriod - 4*ClockHz
	steps := []uint64{start, start + ClockHz, start + 2*ClockHz, PCRBasePeriod - 1, 0, ClockHz}
	wantRel := []uint64{0, ClockHz, 2 * ClockHz, 4*ClockHz - 1, 4 * ClockHz, 4*ClockHz + ClockHz}

	for i, raw := range steps {
		if raw >= PCRBasePeriod {
			raw -= PCRBasePeriod
		}
		got, err := c.ObserveRelative(raw, uint64(i))
		if err != nil {
			t.Fatalf("ObserveRelative(%d): %v", raw, err)
		}
		if got != wantRel[i] {
			t.Fatalf("step %d: relative=%d want %d", i, got, wantRel[i])
		}
	}
	if c.Wraps() != 1 {
		t.Fatalf("wraps=%d want 1", c.Wraps())
	}
}

func TestClockRegressionIsErrorAndDoesNotCommit(t *testing.T) {
	c := NewClock(0x101)
	if _, err := c.Observe(10_000, 1); err != nil {
		t.Fatal(err)
	}
	before := c.State()
	_, err := c.Observe(100, 2)
	if err == nil {
		t.Fatal("expected timestamp regression")
	}
	me, ok := AsMediaError(err)
	if !ok || me.PID != 0x101 || me.Time.End90k == 0 {
		t.Fatalf("error lacks PID/time range: %#v", err)
	}
	if c.State() != before {
		t.Fatal("committed clock state changed after regression")
	}
}
