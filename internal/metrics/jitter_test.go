package metrics

import (
	"math"
	"testing"
	"time"
)

func TestJitterReportsInsufficientSamples(t *testing.T) {
	j := NewJitter()
	base := time.Unix(0, 0)
	for i := 0; i < 3; i++ {
		j.Add(uint32(i*1800), base.Add(time.Duration(i)*20*time.Millisecond))
	}
	s := j.Summary()
	if s.Status != JitterStatusInsufficient {
		t.Fatalf("status=%s", s.Status)
	}
	if s.Samples != 2 {
		t.Fatalf("samples=%d want 2", s.Samples)
	}
	if s.P95 != 0 || s.Max != 0 {
		t.Fatalf("expected zero jitter for perfect spacing, got %+v", s)
	}
}

func TestJitterMeanP95Max(t *testing.T) {
	j := NewJitter()
	base := time.Unix(0, 0)
	// One late packet injects jitter; the rest are evenly spaced.
	for i := 0; i < 40; i++ {
		arrival := base.Add(time.Duration(i) * 20 * time.Millisecond)
		if i == 20 {
			arrival = arrival.Add(5 * time.Millisecond)
		}
		j.Add(uint32(i)*1800, arrival)
	}
	s := j.Summary()
	if s.Status != JitterStatusOK {
		t.Fatalf("status=%s", s.Status)
	}
	if s.Max <= 0 || s.P95 <= 0 || s.Mean <= 0 {
		t.Fatalf("expected positive jitter: %+v", s)
	}
	if s.P95 > s.Max || s.Mean > s.P95 {
		t.Fatalf("ordering violated: %+v", s)
	}
}

func TestJitterHandlesTimestampWrap(t *testing.T) {
	j := NewJitter()
	base := time.Unix(0, 0)
	ts := uint32(math.MaxUint32 - 2*1800)
	for i := 0; i < 20; i++ {
		j.Add(ts+uint32(i*1800), base.Add(time.Duration(i)*20*time.Millisecond))
	}
	s := j.Summary()
	if s.Status != JitterStatusOK {
		t.Fatalf("status=%s", s.Status)
	}
	if s.Max > 1 {
		t.Fatalf("wrap should not be interpreted as jitter: %+v", s)
	}
}
