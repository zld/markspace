package buffer

import (
	"testing"
	"time"

	"jitter/internal/clock"
	"jitter/internal/wire"
)

func pkt(seq uint16) wire.Packet {
	return wire.Packet{
		Seq:       seq,
		Timestamp: uint32(seq) * wire.SamplesPerFrame,
		Samples:   wire.SamplesPerFrame,
		Payload:   make([]byte, wire.PayloadBytes),
	}
}

// pushAt advances the virtual clock to t and pushes seq.
func pushAt(c *Controller, v *clock.Virtual, t time.Duration, seq uint16) {
	v.Set(t)
	c.Push(pkt(seq))
}

// fillTarget pushes enough in-order frames to start playback.
func fillTarget(c *Controller, v *clock.Virtual, n int) {
	for i := 0; i < n; i++ {
		pushAt(c, v, time.Duration(i)*frameInterval, uint16(i))
	}
}

func TestInOrderPlayback(t *testing.T) {
	v := clock.NewVirtual()
	c := New(v)
	fillTarget(c, v, 4)
	for want := uint16(0); want < 4; want++ {
		p, ok := c.Poll()
		if !ok || p.Seq != want {
			t.Fatalf("Poll = seq %d ok %v, want seq %d", p.Seq, ok, want)
		}
	}
	if got := c.Stats().Received; got != 4 {
		t.Fatalf("Received = %d, want 4", got)
	}
}

func TestDuplicateCounted(t *testing.T) {
	v := clock.NewVirtual()
	c := New(v)
	fillTarget(c, v, 3)
	c.Push(pkt(2)) // still buffered -> duplicate
	if got := c.Stats().Duplicates; got != 1 {
		t.Fatalf("Duplicates = %d, want 1", got)
	}
}

func TestLateCounted(t *testing.T) {
	v := clock.NewVirtual()
	c := New(v)
	fillTarget(c, v, 3)
	c.Poll()         // plays seq 0
	c.Push(pkt(0))   // already played -> late
	if got := c.Stats().Late; got != 1 {
		t.Fatalf("Late = %d, want 1", got)
	}
}

func TestReorderAcceptedInWindow(t *testing.T) {
	v := clock.NewVirtual()
	c := New(v)
	pushAt(c, v, 0, 0)
	pushAt(c, v, frameInterval, 2) // skip ahead
	pushAt(c, v, frameInterval, 1) // out of order, within window
	st := c.Stats()
	if st.Reordered != 1 || st.Received != 3 {
		t.Fatalf("Reordered=%d Received=%d, want 1/3", st.Reordered, st.Received)
	}
}

func TestOutOfWindowCounted(t *testing.T) {
	v := clock.NewVirtual()
	c := New(v)
	pushAt(c, v, 0, 0)
	c.Push(pkt(WindowSize + 1))
	if got := c.Stats().OutOfWindow; got != 1 {
		t.Fatalf("OutOfWindow = %d, want 1", got)
	}
}

func TestSequenceWraparound(t *testing.T) {
	v := clock.NewVirtual()
	c := New(v)
	// Timestamps come from a continuous sample clock; only the 16-bit
	// sequence number wraps.
	for i, seq := range []uint16{65534, 65535, 0, 1} {
		p := pkt(seq)
		p.Timestamp = uint32(65534+i) * wire.SamplesPerFrame
		v.Set(time.Duration(i) * frameInterval)
		c.Push(p)
	}
	for want := uint16(65534); ; want++ {
		p, ok := c.Poll()
		if !ok || p.Seq != want {
			t.Fatalf("Poll = seq %d ok %v, want %d", p.Seq, ok, want)
		}
		if want == 1 {
			break
		}
	}
}

func TestMissingFrameSkippedNotBurst(t *testing.T) {
	v := clock.NewVirtual()
	c := New(v)
	pushAt(c, v, 0, 0)
	pushAt(c, v, frameInterval, 1)
	pushAt(c, v, 2*frameInterval, 3) // seq 2 lost
	if _, ok := c.Poll(); !ok {
		t.Fatal("want frame 0")
	}
	if _, ok := c.Poll(); !ok {
		t.Fatal("want frame 1")
	}
	if _, ok := c.Poll(); ok {
		t.Fatal("frame 2 must be a miss")
	}
	p, ok := c.Poll()
	if !ok || p.Seq != 3 {
		t.Fatalf("Poll = seq %d ok %v, want seq 3", p.Seq, ok)
	}
	c.Push(pkt(2)) // arrives after its deadline -> late
	if got := c.Stats().Late; got != 1 {
		t.Fatalf("Late = %d, want 1", got)
	}
}

func TestStallRealigns(t *testing.T) {
	v := clock.NewVirtual()
	c := New(v)
	fillTarget(c, v, 2)
	c.Poll() // drains the buffer
	c.Poll()
	v.Set(stallRealign + time.Millisecond)
	if _, ok := c.Poll(); ok {
		t.Fatal("stalled controller must not emit")
	}
	if got := c.Stats().Realigned; got != 1 {
		t.Fatalf("Realigned = %d, want 1", got)
	}
	// Next packet re-anchors the timeline.
	pushAt(c, v, stallRealign+2*time.Millisecond, 7)
	pushAt(c, v, stallRealign+3*time.Millisecond, 8)
	if p, ok := c.Poll(); !ok || p.Seq != 7 {
		t.Fatalf("after realign Poll = seq %d ok %v, want seq 7", p.Seq, ok)
	}
}

func TestTimestampJumpRealigns(t *testing.T) {
	v := clock.NewVirtual()
	c := New(v)
	fillTarget(c, v, 3)
	jumped := pkt(3)
	jumped.Timestamp += 10 * wire.SamplesPerFrame // timeline hop
	c.Push(jumped)
	if got := c.Stats().Realigned; got != 1 {
		t.Fatalf("Realigned = %d, want 1", got)
	}
	// Re-anchored timeline re-buffers before playing again.
	next := pkt(4)
	next.Timestamp += 10 * wire.SamplesPerFrame // same new timeline
	v.Set(4 * frameInterval)
	c.Push(next)
	if p, ok := c.Poll(); !ok || p.Seq != 3 {
		t.Fatalf("Poll = seq %d ok %v, want re-anchored seq 3", p.Seq, ok)
	}
}

func TestQueueFullDropsWithoutBlocking(t *testing.T) {
	v := clock.NewVirtual()
	c := New(v)
	for i := 0; i < MaxQueueFrames+10; i++ {
		c.Push(pkt(uint16(i)))
	}
	st := c.Stats()
	if st.DroppedFull == 0 {
		t.Fatal("expected DroppedFull > 0")
	}
	if c.Queued() > MaxQueueFrames {
		t.Fatalf("Queued = %d, exceeds cap %d", c.Queued(), MaxQueueFrames)
	}
}

func TestTargetClampedAndGrowsOnMisses(t *testing.T) {
	v := clock.NewVirtual()
	c := New(v)
	if got := c.Target(); got != MinTarget {
		t.Fatalf("initial target = %d, want %d", got, MinTarget)
	}
	fillTarget(c, v, MinTarget)
	// Sustained misses grow the target one frame at a time.
	for i := 0; i < missGrowStreak*3; i++ {
		v.Advance(frameInterval)
		c.Poll()
	}
	if got := c.Target(); got <= MinTarget || got > MaxTarget {
		t.Fatalf("target = %d, want in (%d, %d]", got, MinTarget, MaxTarget)
	}
}
