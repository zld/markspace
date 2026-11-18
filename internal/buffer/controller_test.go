package buffer

import (
	"testing"

	"jitterlab/internal/conceal"
	"jitterlab/internal/packet"
)

func ctlConfig() Config {
	return Config{FrameMs: 20, TargetMs: 80, MaxWaitMs: 240, WindowSize: 64}
}

func newController(t *testing.T) *Controller {
	t.Helper()
	c, err := New(ctlConfig(), 0.6)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	return c
}

func TestConfigValidation(t *testing.T) {
	cases := []struct {
		name string
		cfg  Config
	}{
		{"zero frame", Config{FrameMs: 0, TargetMs: 80, MaxWaitMs: 200, WindowSize: 8}},
		{"negative frame", Config{FrameMs: -20, TargetMs: 80, MaxWaitMs: 200, WindowSize: 8}},
		{"zero target", Config{FrameMs: 20, TargetMs: 0, MaxWaitMs: 200, WindowSize: 8}},
		{"target exceeds wait", Config{FrameMs: 20, TargetMs: 400, MaxWaitMs: 200, WindowSize: 8}},
		{"window zero", Config{FrameMs: 20, TargetMs: 80, MaxWaitMs: 200, WindowSize: 0}},
		{"window over limit", Config{FrameMs: 20, TargetMs: 80, MaxWaitMs: 200, WindowSize: MaxWindowSize + 1}},
		{"max wait below one frame", Config{FrameMs: 20, TargetMs: 20, MaxWaitMs: 10, WindowSize: 8}},
		{"frame over limit", Config{FrameMs: MaxFrameMs + 1, TargetMs: 80, MaxWaitMs: 200, WindowSize: 8}},
	}
	for _, tc := range cases {
		if err := tc.cfg.Validate(); err == nil {
			t.Errorf("%s: config accepted", tc.name)
		}
	}
	if err := ctlConfig().Validate(); err != nil {
		t.Fatalf("valid config rejected: %v", err)
	}
}

// feed pushes count sequential frames with the given inter-arrival gap.
func feed(c *Controller, start uint64, count int, firstAt, gap int64) {
	for i := 0; i < count; i++ {
		seq := start + uint64(i)
		c.Offer(packet.Frame{Version: packet.Version, Seq: seq}, firstAt+int64(i)*gap)
	}
}

func TestControllerStartsAfterTargetLevel(t *testing.T) {
	c := newController(t)
	feed(c, 0, 2, 0, 20)
	if slot := c.Tick(50); slot.Played {
		t.Fatal("played before target level reached")
	}
	feed(c, 2, 8, 60, 20)
	slot := c.Tick(200)
	if !slot.Played || slot.Seq != 0 || slot.Kind != conceal.KindReal {
		t.Fatalf("first slot = %+v", slot)
	}
	if slot.BufferMs <= 0 {
		t.Fatalf("buffer water level = %d, want positive", slot.BufferMs)
	}
}

func TestControllerConcealsOnGapAndResumes(t *testing.T) {
	c := newController(t)
	feed(c, 0, 5, 0, 20)
	c.Offer(packet.Frame{Version: packet.Version, Seq: 9}, 100)
	c.Offer(packet.Frame{Version: packet.Version, Seq: 10}, 120)
	if slot := c.Tick(140); !slot.Played {
		t.Fatal("did not start")
	}
	// Seqs 5..8 were never offered, so playout must conceal across them.
	c.Tick(160)
	c.Tick(180)
	c.Tick(200)
	if slot := c.Tick(220); slot.Seq != 4 || slot.Kind != conceal.KindReal {
		t.Fatalf("slot at 220 = %+v, want real seq 4", slot)
	}
	slot := c.Tick(240)
	if slot.Kind == conceal.KindReal || slot.Seq != 5 {
		t.Fatalf("slot for missing seq 5 = %+v, want concealed", slot)
	}
	if slot.ConcealRun != 1 {
		t.Fatalf("conceal run = %d, want 1", slot.ConcealRun)
	}

	// A late seq 5 must not overwrite the slot already concealed.
	if res := c.Offer(packet.Frame{Version: packet.Version, Seq: 5}, 245); res.Status != Expired {
		t.Fatalf("late seq 5 = %v, want expired", res.Status)
	}

	for i := 0; i < 3; i++ { // conceal seqs 6, 7, 8
		c.Tick(int64(260 + i*20))
	}
	slot = c.Tick(320)
	if slot.Seq != 9 || slot.Kind != conceal.KindReal {
		t.Fatalf("resume slot = %+v, want real seq 9", slot)
	}
	st := c.Snapshot()
	if st.Concealed == 0 || st.Recoveries != 1 {
		t.Fatalf("stats = %+v, want concealment and one recovery", st)
	}
	if st.ContinuitySum < 0 {
		t.Fatalf("continuity sum = %v", st.ContinuitySum)
	}
}

func TestControllerDropsWhenFarAhead(t *testing.T) {
	c := newController(t)
	c.Offer(packet.Frame{Version: packet.Version, Seq: 0}, 0)
	// 200 frames ahead is well outside the 64-frame window.
	if res := c.Offer(packet.Frame{Version: packet.Version, Seq: 200}, 20); res.Status != Expired {
		t.Fatalf("far ahead = %v, want expired", res.Status)
	}
	if st := c.Snapshot(); st.Dropped != 1 {
		t.Fatalf("dropped = %d, want 1", st.Dropped)
	}
}

func TestControllerShedsLatency(t *testing.T) {
	cfg := ctlConfig()
	cfg.TargetMs = 40
	cfg.MaxWaitMs = 100 // max 5 frames buffered
	c, err := New(cfg, 0.6)
	if err != nil {
		t.Fatal(err)
	}
	feed(c, 0, 6, 0, 20)
	if slot := c.Tick(120); !slot.Played {
		t.Fatal("did not start")
	}
	// Bulk arrival builds a large backlog; the playout point should skip
	// ahead instead of growing unbounded latency.
	feed(c, 6, 40, 130, 1)
	for i := 0; i < 5; i++ {
		c.Tick(int64(140 + i*20))
	}
	if st := c.Snapshot(); st.Dropped == 0 {
		t.Fatalf("stats = %+v, want shed drops", st)
	}
}

func TestControllerMaxConcealRunAndFinish(t *testing.T) {
	c := newController(t)
	feed(c, 0, 4, 0, 20)
	c.Tick(100) // below target and not finished: nothing plays yet
	c.Finish()  // end-of-stream lets playout start with what is buffered
	c.Tick(120) // play 0
	c.Tick(140) // play 1
	c.Tick(160) // play 2
	c.Tick(180) // play 3
	if slot := c.Tick(200); slot.Played || !slot.Finished {
		t.Fatalf("drained slot = %+v, want finished without playout", slot)
	}
	st := c.Snapshot()
	if st.PlayoutSlots != 4 {
		t.Fatalf("stats = %+v, want 4 played slots", st)
	}
	if !st.Finished {
		t.Fatalf("stats = %+v, want finished", st)
	}
}

func TestControllerJitterRaisesTarget(t *testing.T) {
	c := newController(t)
	// Erratic arrivals push the smoothed jitter up over time.
	at := int64(0)
	for i := 0; i < 40; i++ {
		at += int64(20 + (i%4)*30)
		c.Offer(packet.Frame{Version: packet.Version, Seq: uint64(i)}, at)
	}
	if c.Snapshot().Started {
		t.Fatal("started without any tick")
	}
	if c.targetFrames <= ceilDiv(ctlConfig().TargetMs, ctlConfig().FrameMs) {
		t.Fatalf("target frames = %d, want above base after jitter", c.targetFrames)
	}
	if c.targetFrames > c.maxFrames {
		t.Fatalf("target frames = %d exceeds max %d", c.targetFrames, c.maxFrames)
	}
}
