package bitrate

import "testing"

func testConfig() Config {
	return Config{
		TargetBps:       2_500_000,
		MinBps:          250_000,
		SlewUpPct:       25,
		SlewDownPct:     25,
		EWMAAlpha:       0.5,
		InterruptTicks:  500,
		TimeoutTicks:    2_000,
		ChunkDurationMs: 100,
	}
}

func TestSlewLimitsSuddenChange(t *testing.T) {
	c, err := NewController(testConfig())
	if err != nil {
		t.Fatal(err)
	}
	// Sudden drop from 2.5 Mbps to 500 kbps: one decision may fall at most 25%.
	if err := c.Observe(Sample{AtTick: 0, BandwidthBps: 500_000}); err != nil {
		t.Fatal(err)
	}
	st := c.Decide(0)
	// ceil(2.5M * 0.25) = 625k, so floor 1.875M; estimate 500k pulls toward min.
	if st.TargetBps != 1_875_000 {
		t.Fatalf("first decision = %d, want 1875000", st.TargetBps)
	}
	// Continued pressure converges toward the measured estimate (500k),
	// staying above the absolute floor as long as the link carries traffic.
	prev := st.TargetBps
	for i := 0; i < 12; i++ {
		st = c.Decide(int64(i+1) * 100)
		if st.TargetBps > prev {
			t.Fatalf("target rose under pressure: %d -> %d", prev, st.TargetBps)
		}
		prev = st.TargetBps
	}
	if prev != 500_000 {
		t.Fatalf("converged target = %d, want measured 500000", prev)
	}
	// Only an explicit zero reading forces the absolute floor.
	_ = c.Observe(Sample{AtTick: 1300, BandwidthBps: 0})
	st = c.Decide(1300)
	if st.TargetBps != 250_000 {
		t.Fatalf("zero floor = %d, want 250000", st.TargetBps)
	}
}

func TestSlewUpIsAlsoLimited(t *testing.T) {
	c, _ := NewController(testConfig())
	// Drive to the floor first.
	for i := 0; i < 15; i++ {
		_ = c.Observe(Sample{AtTick: int64(i) * 100, BandwidthBps: 250_000})
		_ = c.Decide(int64(i+1) * 100)
	}
	if c.current != 250_000 {
		t.Fatalf("setup: current = %d", c.current)
	}
	// Bandwidth fully restored; ramp-up is capped per decision.
	_ = c.Observe(Sample{AtTick: 1600, BandwidthBps: 2_500_000})
	st := c.Decide(1600)
	if st.TargetBps != 312_500 { // 250k + ceil(25%*250k)
		t.Fatalf("ramp step = %d, want 312500", st.TargetBps)
	}
}

func TestZeroBandwidthIsDeterministic(t *testing.T) {
	c, _ := NewController(testConfig())
	if err := c.Observe(Sample{AtTick: 0, BandwidthBps: 0}); err != nil {
		t.Fatal(err)
	}
	st := c.Decide(0)
	if !st.Degraded || st.TargetBps != c.cfg.MinBps {
		t.Fatalf("zero bw state = %+v", st)
	}
	// Repeated decisions stay at the floor — no fabricated recovery.
	for i := 1; i < 5; i++ {
		again := c.Decide(int64(i) * 100)
		if again.TargetBps != c.cfg.MinBps || !again.Degraded {
			t.Fatalf("zero bw decision %d drifted: %+v", i, again)
		}
	}
	if st.DeltaPercent() != 100 {
		t.Fatalf("zero-bw delta = %d, want 100", st.DeltaPercent())
	}
}

func TestInterruptionAndTimeout(t *testing.T) {
	c, _ := NewController(testConfig())
	_ = c.Observe(Sample{AtTick: 0, BandwidthBps: 2_000_000})
	_ = c.Decide(0)

	interrupted := c.Decide(600) // 600 ms silence > InterruptTicks
	if !interrupted.Degraded || interrupted.TimedOut {
		t.Fatalf("interrupt state = %+v", interrupted)
	}
	timedOut := c.Decide(2_100) // > TimeoutTicks
	if !timedOut.TimedOut || timedOut.TargetBps != c.cfg.MinBps {
		t.Fatalf("timeout state = %+v", timedOut)
	}
}

func TestNoTelemetryIsDegradedButStable(t *testing.T) {
	c, _ := NewController(testConfig())
	st := c.Decide(0)
	if !st.Degraded || st.TargetBps != testConfig().TargetBps {
		t.Fatalf("no-telemetry state = %+v", st)
	}
	if st.ActualBps != 0 || st.DeltaPercent() != 100 {
		t.Fatalf("missing actual should report 100%% delta: %+v", st)
	}
}

func TestChunkBudgetAndDelta(t *testing.T) {
	c, _ := NewController(testConfig())
	_ = c.Observe(Sample{AtTick: 0, BandwidthBps: 2_000_000})
	st := c.Decide(0)
	// Budget at the slew-limited target for a 100 ms chunk.
	wantBudget := st.TargetBps * 100 / 1000
	if got := c.ChunkBudget(); got != wantBudget {
		t.Fatalf("budget = %d, want %d", got, wantBudget)
	}
	// Actual 2.0 Mbps against a 2.5 Mbps target => -20%.
	_ = c.Observe(Sample{AtTick: 100, BandwidthBps: 2_000_000})
	st = c.Decide(100)
	delta := st.DeltaPercent()
	if delta < 0 || delta > 25 {
		t.Fatalf("delta = %d%% outside plausible range", delta)
	}
}

func TestRejectsBadInput(t *testing.T) {
	if _, err := NewController(Config{TargetBps: 0}); err == nil {
		t.Fatal("zero target must be rejected")
	}
	c, _ := NewController(testConfig())
	if err := c.Observe(Sample{AtTick: 0, BandwidthBps: -1}); err == nil {
		t.Fatal("negative bandwidth must be rejected")
	}
	_ = c.Observe(Sample{AtTick: 100, BandwidthBps: 1_000})
	if err := c.Observe(Sample{AtTick: 50, BandwidthBps: 1_000}); err == nil {
		t.Fatal("backwards sample time must be rejected")
	}
}

func TestNeverExceedsServiceTarget(t *testing.T) {
	cfg := testConfig()
	c, _ := NewController(cfg)
	_ = c.Observe(Sample{AtTick: 0, BandwidthBps: 10_000_000})
	for i := 0; i < 10; i++ {
		st := c.Decide(int64(i) * 100)
		if st.TargetBps > cfg.TargetBps {
			t.Fatalf("decision %d overshot target: %d", i, st.TargetBps)
		}
	}
}
