package rtp

import (
	"testing"
	"time"
)

func TestJitterTargetDirectionAndClamp(t *testing.T) {
	now := time.Unix(1, 0)

	// Fresh buffer: first sample at the bandwidth floor -> deepest target.
	jLow := NewJitterBuffer(DefaultJitterConfig())
	lo := jLow.Observe(BandwidthSample{T: now, BytesPerSecond: jLow.cfg.BandwidthFloor})
	if lo.Target != jLow.cfg.MaxTarget || !lo.ClampedLow {
		t.Fatalf("floor target = %+v, want max %d", lo, jLow.cfg.MaxTarget)
	}

	// Separate fresh buffer: first sample at the ceiling -> shallowest.
	jHigh := NewJitterBuffer(DefaultJitterConfig())
	hi := jHigh.Observe(BandwidthSample{T: now, BytesPerSecond: jHigh.cfg.BandwidthCeil})
	if hi.Target != jHigh.cfg.MinTarget || !hi.ClampedHigh {
		t.Fatalf("ceiling target = %+v, want min %d", hi, jHigh.cfg.MinTarget)
	}

	// Everything must stay within configured bounds.
	for _, s := range append(jLow.Samples, jHigh.Samples...) {
		if s.Target < jLow.cfg.MinTarget || s.Target > jLow.cfg.MaxTarget {
			t.Fatalf("target %d outside bounds", s.Target)
		}
	}
}

func TestJitterOutOfRangeClamps(t *testing.T) {
	j := NewJitterBuffer(DefaultJitterConfig())
	below := j.Observe(BandwidthSample{BytesPerSecond: 1}) // below floor
	if below.Target != j.cfg.MaxTarget || !below.ClampedLow || below.ClampedHigh {
		t.Fatalf("below-floor decision wrong: %+v", below)
	}
	above := j.Observe(BandwidthSample{BytesPerSecond: 1 << 40}) // above ceiling
	if above.Target != j.cfg.MinTarget || !above.ClampedHigh || above.ClampedLow {
		t.Fatalf("above-ceiling decision wrong: %+v", above)
	}
}

func TestJitterEMASmoothsBursts(t *testing.T) {
	cfg := DefaultJitterConfig()
	cfg.EMAAlpha = 50 // equal weight, easy to reason about
	j := NewJitterBuffer(cfg)
	j.Observe(BandwidthSample{BytesPerSecond: 1_000_000})
	mid := j.Observe(BandwidthSample{BytesPerSecond: 2_000_000})
	// EMA(50%): (1e6 + 2e6)/2 = 1.5e6
	if mid.SmoothedBPS != 1_500_000 {
		t.Fatalf("smoothed = %d, want 1500000", mid.SmoothedBPS)
	}
	// A single zero sample must not collapse the target instantly.
	j.Observe(BandwidthSample{BytesPerSecond: 0})
	if j.Target() < j.cfg.MinTarget || j.Target() > j.cfg.MaxTarget {
		t.Fatalf("target out of bounds after zero sample: %d", j.Target())
	}
	if j.SmoothedBandwidth() != 750_000 {
		t.Fatalf("smoothed after zero = %d, want 750000", j.SmoothedBandwidth())
	}
}

func TestJitterLevelTracking(t *testing.T) {
	j := NewJitterBuffer(DefaultJitterConfig())
	for _, n := range []int{5, 12, 3, 9} {
		j.SetLevel(n)
	}
	if j.Level() != 9 || j.MinLevel != 3 || j.MaxLevel != 12 {
		t.Fatalf("level extrema wrong: level=%d min=%d max=%d", j.Level(), j.MinLevel, j.MaxLevel)
	}
	j.SetLevel(0)
	if !j.Underflow() {
		t.Fatal("zero occupancy against positive target must report underflow")
	}
	j.SetLevel(j.Target())
	if j.Underflow() {
		t.Fatal("occupancy at target must not report underflow")
	}
}

func TestJitterBadConfigFallsBack(t *testing.T) {
	j := NewJitterBuffer(JitterConfig{MinTarget: 10, MaxTarget: 2, EMAAlpha: 0})
	d := DefaultJitterConfig()
	if j.cfg.MinTarget != d.MinTarget || j.cfg.MaxTarget != d.MaxTarget || j.cfg.EMAAlpha != d.EMAAlpha {
		t.Fatalf("bad config not normalized: %+v", j.cfg)
	}
	if j.Target() != d.StartTarget {
		t.Fatalf("start target = %d, want %d", j.Target(), d.StartTarget)
	}
}

func TestJitterMonotoneAcrossBandwidth(t *testing.T) {
	j := NewJitterBuffer(DefaultJitterConfig())
	prev := j.cfg.MaxTarget
	for _, bps := range []int64{100_000, 250_000, 500_000, 1_000_000, 2_000_000, 4_000_000, 7_000_000} {
		ts := j.Observe(BandwidthSample{BytesPerSecond: bps})
		if ts.Target > prev {
			t.Fatalf("target grew as bandwidth rose: bps=%d target=%d prev=%d", bps, ts.Target, prev)
		}
		prev = ts.Target
	}
}
