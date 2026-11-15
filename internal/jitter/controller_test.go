package jitter

import (
	"testing"
	"time"

	"jbbench/internal/protocol"
)

func frame() time.Duration { return protocol.FrameDurationNS() }

func TestInitialLevelDuringWarmup(t *testing.T) {
	cfg := DefaultControllerConfig()
	c := NewController(cfg)
	if c.Level() != cfg.InitialLevel {
		t.Fatalf("initial level = %v, want %v", c.Level(), cfg.InitialLevel)
	}
	// Fewer than Warmup observations: level must stay at the initial watermark.
	for i := 1; i < cfg.Warmup; i++ {
		c.ObserveArrival(time.Duration(i) * frame())
		if c.Level() != cfg.InitialLevel {
			t.Fatalf("level changed during warmup at sample %d: %v", i, c.Level())
		}
	}
}

func TestLevelBoundedByMinMax(t *testing.T) {
	cfg := DefaultControllerConfig()
	cfg.Warmup = 2
	cfg.MaxStep = 2 * frame()
	c := NewController(cfg)
	// Huge latency spikes must never push the watermark past MaxLevel.
	for i := 1; i <= 400; i++ {
		gap := frame()
		if i%3 == 0 {
			gap = 100 * frame()
		}
		c.ObserveArrival(time.Duration(i) * gap)
		if c.Level() < cfg.MinLevel || c.Level() > cfg.MaxLevel {
			t.Fatalf("level %v out of bounds [%v,%v] at %d",
				c.Level(), cfg.MinLevel, cfg.MaxLevel, i)
		}
	}
	if c.Level() != cfg.MaxLevel {
		t.Fatalf("level = %v, want clamped max %v", c.Level(), cfg.MaxLevel)
	}
}

func TestMaxStepLimitsSingleAdjustment(t *testing.T) {
	cfg := DefaultControllerConfig()
	cfg.Warmup = 1
	cfg.MaxStep = frame()
	c := NewController(cfg)
	// One giant deviation: adaptation may move at most MaxStep.
	c.ObserveArrival(0)
	c.ObserveArrival(50 * frame())
	if got := c.Level() - cfg.InitialLevel; got > cfg.MaxStep {
		t.Fatalf("single adjustment %v exceeds MaxStep %v", got, cfg.MaxStep)
	}
}

func TestConfigClamping(t *testing.T) {
	cfg := DefaultControllerConfig()
	cfg.MinLevel = 10 * frame()
	cfg.MaxLevel = frame() // inverted
	cfg.InitialLevel = -5
	cfg.Smooth = 7
	cfg.MaxStep = -1
	c := NewController(cfg)
	eff := c.Config()
	if eff.MaxLevel < eff.MinLevel {
		t.Fatal("max level not clamped above min")
	}
	if eff.InitialLevel < eff.MinLevel || eff.InitialLevel > eff.MaxLevel {
		t.Fatal("initial level out of bounds")
	}
	if eff.Smooth <= 0 || eff.Smooth > 1 {
		t.Fatalf("smooth factor not clamped: %v", eff.Smooth)
	}
	if eff.MaxStep <= 0 {
		t.Fatal("max step not defaulted")
	}
}

func TestRegularStreamSettlesNearInitial(t *testing.T) {
	cfg := DefaultControllerConfig()
	c := NewController(cfg)
	for i := 1; i <= 300; i++ {
		c.ObserveArrival(time.Duration(i) * frame())
	}
	// No deviation: watermark must converge to the initial level, not grow.
	if c.Level() != cfg.InitialLevel {
		t.Fatalf("level = %v, want %v", c.Level(), cfg.InitialLevel)
	}
	if c.Jitter() != 0 {
		t.Fatalf("jitter = %v, want 0", c.Jitter())
	}
}
