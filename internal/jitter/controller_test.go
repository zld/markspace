package jitter

import (
	"testing"
	"time"
)

func testConfig() Config {
	return Config{
		MinTarget:      2,
		MaxTarget:      12,
		InitTarget:     3,
		FramePeriod:    20 * time.Millisecond,
		ExpandStep:     2,
		ShrinkInterval: 100 * time.Millisecond,
		JitterHigh:     1.5,
	}
}

func TestControllerAddAndTick(t *testing.T) {
	c := NewController(testConfig())
	base := time.Now()
	c.now = func() time.Time { return base }
	c.Add(Frame{Seq: 0, PCM: []byte{1}})
	c.Add(Frame{Seq: 1, PCM: []byte{2}})
	if s := c.Snapshot(); s.Queued != 2 {
		t.Fatalf("queued %d", s.Queued)
	}
	f, ok := c.Tick()
	if !ok || f.Seq != 0 {
		t.Fatalf("tick: %+v ok=%v", f, ok)
	}
	st := c.Stats()
	if st.Played != 1 || st.Received != 2 {
		t.Fatalf("stats %+v", st)
	}
	// Drain the remaining frame, then a miss -> conceal.
	if _, ok := c.Tick(); !ok {
		t.Fatal("expected queued frame")
	}
	if _, ok := c.Tick(); ok {
		t.Fatal("expected miss")
	}
	if c.Stats().Concealed != 1 {
		t.Fatal("conceal not counted")
	}
}

func TestControllerExpandsOnJitter(t *testing.T) {
	c := NewController(testConfig())
	base := time.Now()
	// Feed steady arrivals to settle EMA, then a burst gap.
	for i := 0; i < 6; i++ {
		at := base.Add(time.Duration(i) * 20 * time.Millisecond)
		c.now = func() time.Time { return at }
		c.Add(Frame{Seq: uint16(i)})
	}
	before := c.Snapshot().Target
	for i := 6; i < 12; i++ {
		at := base.Add(120*time.Millisecond + time.Duration(i-6)*90*time.Millisecond)
		c.now = func() time.Time { return at }
		c.Add(Frame{Seq: uint16(i)})
	}
	after := c.Snapshot().Target
	if after <= before {
		t.Fatalf("target did not expand: %d -> %d", before, after)
	}
	if after > c.cfg.MaxTarget {
		t.Fatalf("target exceeds cap: %d", after)
	}
}

func TestControllerShrinksWhenStable(t *testing.T) {
	c := NewController(testConfig())
	base := time.Now()
	at := base
	for i := 0; i < 40; i++ {
		at = at.Add(20 * time.Millisecond)
		c.now = func() time.Time { return at }
		c.Add(Frame{Seq: uint16(i)})
		c.Tick()
	}
	if got := c.Snapshot().Target; got != c.cfg.MinTarget {
		t.Fatalf("target %d, want min %d", got, c.cfg.MinTarget)
	}
}

func TestControllerClassifiesDrops(t *testing.T) {
	c := NewController(testConfig())
	c.now = time.Now
	c.Add(Frame{Seq: 5})
	c.Add(Frame{Seq: 5}) // duplicate
	c.Tick()             // consume 5
	c.Add(Frame{Seq: 4}) // late
	c.NoteMalformed()
	st := c.Stats()
	if st.Duplicate != 1 || st.Late != 1 || st.Malformed != 1 {
		t.Fatalf("stats %+v", st)
	}
}
