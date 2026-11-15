package jitter

import "testing"

func TestControllerStaysWithinBounds(t *testing.T) {
	c := NewController(Config{FrameMS: 20, Target: 3, MinLevel: 1, MaxLevel: 8})
	// Steady arrivals with mild jitter.
	for i := 0; i < 200; i++ {
		c.Observe(int64(45+i*20+i%3), uint32(i*20))
		if lvl := c.TargetLevel(); lvl < 1 || lvl > 8 {
			t.Fatalf("level %d out of bounds at i=%d", lvl, i)
		}
	}
	if c.JitterMS() < 0 {
		t.Fatalf("negative jitter %v", c.JitterMS())
	}
}

func TestControllerClockRegressionAndIdleGap(t *testing.T) {
	c := NewController(Config{FrameMS: 20, Target: 3, MinLevel: 1, MaxLevel: 6})
	for i := 0; i < 20; i++ {
		c.Observe(int64(100+i*20), uint32(i*20))
	}
	// Clock steps backwards sharply, then a long idle gap.
	c.Observe(10, uint32(20*20))
	c.Observe(100000, uint32(21*20))
	if c.JitterMS() < 0 {
		t.Fatalf("negative jitter after regression: %v", c.JitterMS())
	}
	if lvl := c.TargetLevel(); lvl < 1 || lvl > 6 {
		t.Fatalf("level %d out of bounds after regression", lvl)
	}
	// Jitter must be clamped: a 100s idle gap cannot explode the level.
	for i := 0; i < 50; i++ {
		c.Observe(int64(100100+i*20), uint32((22+i)*20))
		if c.TargetLevel() > 6 {
			t.Fatalf("unbounded growth: level %d", c.TargetLevel())
		}
	}
}

func TestControllerAdaptsUpwardWithJitter(t *testing.T) {
	c := NewController(Config{FrameMS: 20, Target: 2, MinLevel: 1, MaxLevel: 10})
	base := int64(0)
	for i := 0; i < 400; i++ {
		// Alternate +/-40ms transit jitter.
		off := int64(40)
		if i%2 == 0 {
			off = -40
		}
		c.Observe(base+int64(i)*20+60+off, uint32(i*20))
	}
	if c.TargetLevel() <= 2 {
		t.Fatalf("target did not adapt upward: %d", c.TargetLevel())
	}
}

func TestWindowWraparound(t *testing.T) {
	w := NewWindow(8)
	for _, seq := range []uint16{65534, 65535, 0, 1} {
		if r := w.Insert(seq, []int16{int16(seq)}); r != "" {
			t.Fatalf("insert %d dropped: %s", seq, r)
		}
	}
	if !Less(65535, 0) || Less(0, 65535) {
		t.Fatal("wrap comparison broken")
	}
	if got := w.Depth(65534); got != 4 {
		t.Fatalf("depth %d want 4", got)
	}
	if _, ok := w.Pop(0); !ok {
		t.Fatal("missing wrapped seq 0")
	}
}

func TestWindowDuplicateDoesNotOverwrite(t *testing.T) {
	w := NewWindow(8)
	w.Insert(5, []int16{1, 1, 1})
	if r := w.Insert(5, []int16{9, 9, 9}); r != DropDuplicate {
		t.Fatalf("reason %q want duplicate", r)
	}
	s, _ := w.Pop(5)
	if s[0] != 1 {
		t.Fatalf("duplicate overwrote frame: %v", s)
	}
}

func TestWindowLateTooOldFull(t *testing.T) {
	w := NewWindow(4)
	w.Insert(10, []int16{1})
	w.Insert(11, []int16{1})
	w.Insert(12, []int16{1})
	w.Insert(13, []int16{1})

	w.SetPlayed(10)
	if r := w.Insert(9, []int16{2}); r != DropLate {
		t.Fatalf("reason %q want late", r)
	}
	if r := w.Insert(5, []int16{2}); r != DropTooOld && r != DropLate {
		t.Fatalf("reason %q want too_old", r)
	}
	if r := w.Insert(14, []int16{2}); r != DropFull {
		t.Fatalf("reason %q want window_full", r)
	}
	if got := w.Depth(0); got != 4 {
		t.Fatalf("depth %d want 4 after full drop", got)
	}
}

func TestWindowOutOfOrder(t *testing.T) {
	w := NewWindow(8)
	for _, seq := range []uint16{3, 1, 2, 0} {
		if r := w.Insert(seq, nil); r != "" {
			t.Fatalf("reordered insert %d dropped: %s", seq, r)
		}
	}
	for i := uint16(0); i < 4; i++ {
		if _, ok := w.Pop(i); !ok {
			t.Fatalf("seq %d not buffered", i)
		}
	}
}
