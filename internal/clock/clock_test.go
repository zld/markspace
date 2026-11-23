package clock

import (
	"errors"
	"testing"
	"time"
)

func TestClockTicksAndMillis(t *testing.T) {
	c, err := New(90000)
	if err != nil {
		t.Fatal(err)
	}
	if got := c.Ticks(time.Second); got != 90000 {
		t.Fatalf("1s = %d ticks, want 90000", got)
	}
	if got := c.Ticks(20 * time.Millisecond); got != 1800 {
		t.Fatalf("20ms = %d ticks, want 1800", got)
	}
	if got := c.Millis(1800); got != 20 {
		t.Fatalf("1800 ticks = %d ms, want 20", got)
	}
	if got := c.SegmentTicks(2 * time.Second); got != 180000 {
		t.Fatalf("segment ticks = %d, want 180000", got)
	}
	if _, err := New(0); err == nil {
		t.Fatal("zero timescale must error")
	}
}

func TestScaleNonDivisibleRounding(t *testing.T) {
	// 30000 -> 90000 is exact; 90000 -> 30000 must round.
	cases := []struct {
		ts       int64
		src, dst uint32
		want     int64
	}{
		{1, 30000, 90000, 3},
		{1, 90000, 30000, 0}, // 1/3 rounds down
		{2, 90000, 30000, 1}, // 2/3 rounds up
		{7, 90000, 25, 0},
	}
	for _, tc := range cases {
		got, err := Scale(tc.ts, tc.src, tc.dst)
		if err != nil {
			t.Fatal(err)
		}
		if got != tc.want {
			t.Errorf("Scale(%d, %d, %d) = %d, want %d", tc.ts, tc.src, tc.dst, got, tc.want)
		}
	}
	if _, err := Scale(1, 0, 10); err == nil {
		t.Fatal("zero source scale must error")
	}
}

func TestConverterMonotonicRemainder(t *testing.T) {
	cv, err := NewConverter(30000, 44100)
	if err != nil {
		t.Fatal(err)
	}
	var prev int64 = -1
	for i := int64(0); i <= 1000; i++ {
		out, err := cv.Push(i) // one tick per push; first push anchors at 0
		if err != nil {
			t.Fatalf("push %d: %v", i, err)
		}
		if out < prev {
			t.Fatalf("non-monotonic output at %d: %d < %d", i, out, prev)
		}
		prev = out
	}
	// Total: 1000 src ticks * 44100/30000 = 1470 dst ticks exactly.
	if prev != 1470 {
		t.Fatalf("accumulated = %d, want 1470 (no drift, no float)", prev)
	}
}

func TestConverterDuplicateAndReorder(t *testing.T) {
	cv, _ := NewConverter(90000, 30000)
	first, err := cv.Push(100)
	if err != nil {
		t.Fatal(err)
	}
	dup, err := cv.Push(100)
	if err == nil {
		t.Fatal("duplicate must return an error")
	}
	var ce *Error
	if !errors.As(err, &ce) || !ce.Recoverable || ce.Stage != StageClock {
		t.Fatalf("want recoverable clock error, got %v", err)
	}
	if dup != first || cv.Duplicates != 1 {
		t.Fatalf("duplicate handling wrong: dup=%d first=%d count=%d", dup, first, cv.Duplicates)
	}
	if _, err := cv.Push(99); err == nil || cv.Reordered != 1 {
		t.Fatal("backwards timestamp must be counted and flagged")
	}
	good, err := cv.Push(103)
	if err != nil {
		t.Fatal(err)
	}
	if good <= first {
		t.Fatal("output must advance after recovery")
	}
}

func TestRTPUnwrapperWraparound(t *testing.T) {
	u := NewRTPUnwrapper()
	first, err := u.Next(0xFFFFFFF0)
	if err != nil {
		t.Fatal(err)
	}
	second, err := u.Next(0xFFFFFFFC)
	if err != nil {
		t.Fatal(err)
	}
	third, err := u.Next(0x00000004) // wraps 2^32
	if err != nil {
		t.Fatal(err)
	}
	if second <= first || third <= second {
		t.Fatal("unwrapped timeline must be monotonic")
	}
	if third-first != 20 {
		t.Fatalf("span across wrap = %d, want 20", third-first)
	}
	if _, err := u.Next(0x00000004); err == nil || u.Duplicates != 1 {
		t.Fatal("duplicate RTP timestamp must be flagged")
	}
	if _, err := u.Next(0x00000002); err == nil || u.Backwards != 1 {
		t.Fatal("backwards RTP timestamp must be flagged")
	}
}

func TestSeqTrackerLossDuplicateReorder(t *testing.T) {
	s := &SeqTracker{}
	st, lost := s.Next(10)
	if st != StatusOK || lost != 0 {
		t.Fatal("first packet must be clean")
	}
	if st, lost = s.Next(13); st != StatusOK || lost != 2 {
		t.Fatalf("gap of 2: status=%d lost=%d", st, lost)
	}
	if st, _ = s.Next(13); st != StatusDuplicate || s.Duplicates != 1 {
		t.Fatalf("duplicate classification failed: %d", st)
	}
	if st, _ = s.Next(11); st != StatusReordered || s.Reordered != 1 {
		t.Fatalf("reorder classification failed: %d", st)
	}
	if s.Lost != 2 {
		t.Fatalf("lost total = %d, want 2", s.Lost)
	}
	// 16-bit rollover, exercised on a fresh tracker near the modulus edge.
	roll := &SeqTracker{}
	if st, lost = roll.Next(65533); st != StatusOK || lost != 0 {
		t.Fatalf("rollover anchor: status=%d lost=%d", st, lost)
	}
	if st, lost = roll.Next(65534); st != StatusOK || lost != 0 {
		t.Fatalf("rollover next: status=%d lost=%d", st, lost)
	}
	if st, lost = roll.Next(2); st != StatusOK || lost != 3 { // 65535,0,1 missing
		t.Fatalf("rollover gap: status=%d lost=%d", st, lost)
	}
}

func TestAlign(t *testing.T) {
	if got := AlignDown(181234, 180000); got != 180000 {
		t.Fatalf("AlignDown = %d", got)
	}
	if got := AlignUp(181234, 180000); got != 360000 {
		t.Fatalf("AlignUp = %d", got)
	}
	if got := AlignDown(180000, 180000); got != 180000 {
		t.Fatalf("exact boundary AlignDown = %d", got)
	}
	if got := AlignUp(180000, 180000); got != 180000 {
		t.Fatalf("exact boundary AlignUp = %d", got)
	}
}
