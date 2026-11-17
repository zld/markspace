package jitter

import (
	"testing"

	"jbcheck/internal/metrics"
	"jbcheck/internal/packet"
	"jbcheck/internal/plc"
)

func newTestController() (*Controller, *metrics.Stats) {
	st := metrics.New(100)
	return NewController(DefaultConfig(), st, plc.New(1)), st
}

func pkt(seq uint16) (packet.Packet, []int16) {
	pcm := make([]int16, plc.FrameSamples)
	for i := range pcm {
		pcm[i] = int16(seq) * 10
	}
	return packet.Packet{Seq: seq, Timestamp: uint32(seq) * 480}, pcm
}

// feed delivers frames first..first+n-1 at 10 ms intervals starting at t=0,
// ticking the playout clock as it goes.
func feed(c *Controller, first uint16, n int) {
	for i := 0; i < n; i++ {
		now := int64(i * 10)
		p, pcm := pkt(first + uint16(i))
		c.Tick(now)
		c.Add(p, pcm, now)
	}
}

func TestPrefillAndPlayout(t *testing.T) {
	c, st := newTestController()
	feed(c, 0, 10)
	if !c.Started() {
		t.Fatal("controller should have started after prefill")
	}
	if st.Played == 0 {
		t.Fatal("no frames played")
	}
	if st.NetworkLost != 0 || st.PLCFrames != 0 {
		t.Fatalf("unexpected loss: lost=%d plc=%d", st.NetworkLost, st.PLCFrames)
	}
}

func TestGapProducesPLCAndLateDrop(t *testing.T) {
	c, st := newTestController()
	feed(c, 0, 5) // start playout
	// Skip seq 5, deliver 6..: frame 5 is concealed at its deadline,
	// then dropped as late when it finally arrives.
	for i := 6; i < 12; i++ {
		now := int64((i - 1) * 10)
		p, pcm := pkt(uint16(i))
		c.Tick(now)
		c.Add(p, pcm, now)
	}
	c.Tick(120) // play out buffered frames, no more
	if st.NetworkLost != 1 || st.PLCFrames != 1 {
		t.Fatalf("lost=%d plc=%d, want 1/1", st.NetworkLost, st.PLCFrames)
	}
	p, pcm := pkt(5)
	c.Add(p, pcm, 200)
	if st.LateDrop != 1 {
		t.Fatalf("late=%d, want 1", st.LateDrop)
	}
	if len(st.Jumps) != 1 {
		t.Fatalf("jumps=%d, want 1 resume jump", len(st.Jumps))
	}
}

func TestDuplicateAndConflict(t *testing.T) {
	c, st := newTestController()
	p, pcm := pkt(0)
	c.Add(p, pcm, 0)
	c.Add(p, pcm, 1) // identical: duplicate
	if st.DuplicateDrop != 1 {
		t.Fatalf("dup=%d, want 1", st.DuplicateDrop)
	}
	other := make([]int16, plc.FrameSamples) // same seq, different payload
	for i := range other {
		other[i] = 999
	}
	c.Add(p, other, 2)
	if st.ConflictDrop != 1 {
		t.Fatalf("conflict=%d, want 1", st.ConflictDrop)
	}
}

func TestOverflowDrop(t *testing.T) {
	cfg := DefaultConfig()
	cfg.QueueCap = 4
	st := metrics.New(100)
	c := NewController(cfg, st, plc.New(1))
	// Fill the queue; without ticks nothing is played out.
	for i := 0; i < 4; i++ {
		p, pcm := pkt(uint16(i))
		c.Add(p, pcm, int64(i))
	}
	if c.QueueLen() != 4 {
		t.Fatalf("queue len=%d, want 4", c.QueueLen())
	}
	p, pcm := pkt(4)
	c.Add(p, pcm, 10)
	if st.OverflowDrop != 1 {
		t.Fatalf("overflow=%d, want 1", st.OverflowDrop)
	}
}

func TestTargetStaysInBounds(t *testing.T) {
	c, _ := newTestController()
	// Large jitter should push the target up, but never past 12.
	for i := 0; i < 200; i++ {
		p, pcm := pkt(uint16(i))
		c.Add(p, pcm, int64(i*10)+(int64(i%2)*200))
	}
	if c.Target() < 2 || c.Target() > 12 {
		t.Fatalf("target=%d out of [2,12]", c.Target())
	}
}

func TestSilenceTriggersRebuffer(t *testing.T) {
	c, _ := newTestController()
	feed(c, 0, 10)
	c.Tick(500) // drain
	c.Tick(500 + DefaultConfig().SilenceMs + 50)
	if c.Started() {
		t.Fatal("long silence should reset to prefill")
	}
}

func TestClockJumpReanchors(t *testing.T) {
	c, st := newTestController()
	feed(c, 0, 10)
	played := st.Played
	c.Tick(100000) // huge forward jump
	c.Tick(100010)
	if st.Played <= played {
		t.Fatal("playout should resume immediately after clock jump")
	}
}
