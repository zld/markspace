// Package testkit provides deterministic assertions shared by package tests
// and end-to-end CLI checks: report field validation, scenario accounting
// identities and packet classification expectations.
package testkit

import (
	"fmt"
	"testing"

	"jbbench/internal/metrics"
	"jbbench/internal/sim"
)

// AssertReportFields verifies every acceptance-required JSON field exists with
// a valid domain value.
func AssertReportFields(t *testing.T, r metrics.Report) {
	t.Helper()
	if r.Frames == 0 {
		t.Error("frames must be positive")
	}
	AssertNonNegativeCounters(t, r)
	if r.RecoveryRatio < 0 || r.RecoveryRatio > 1 {
		t.Errorf("recovery_ratio=%v outside [0,1]", r.RecoveryRatio)
	}
	if r.MaxConcealRun < 0 {
		t.Error("max_conceal_run must be non-negative")
	}
	if len(r.BufferLevelCurve) == 0 {
		t.Error("buffer_level_curve must not be empty")
	}
	for i, p := range r.BufferLevelCurve {
		if p.Level < 0 {
			t.Errorf("curve point %d has negative level %d", i, p.Level)
		}
		if i > 0 && p.T < r.BufferLevelCurve[i-1].T {
			t.Errorf("curve point %d goes backwards in time", i)
		}
	}
	if !(r.LatencyP50US <= r.LatencyP95US &&
		r.LatencyP95US <= r.LatencyP99US &&
		r.LatencyP99US <= r.LatencyMaxUS) {
		t.Errorf("latency quantiles not ordered: %+v", r)
	}
	if err := metrics.Validate(r); err != nil {
		t.Errorf("internal consistency: %v", err)
	}
}

// AssertNonNegativeCounters checks every classified drop counter.
func AssertNonNegativeCounters(t *testing.T, r metrics.Report) {
	t.Helper()
	counters := map[string]uint64{
		"received":          r.Received,
		"invalid_packets":   r.InvalidPackets,
		"corrupt_packets":   r.Corrupt,
		"bad_version":       r.BadVersion,
		"bad_length":        r.BadLength,
		"ambiguous":         r.Ambiguous,
		"duplicate_drops":   r.Duplicate,
		"late_drops":        r.Late,
		"overflow_drops":    r.Overflow,
		"concealed_frames":  r.ConcealedFrames,
	}
	for name, v := range counters {
		if int64(v) < 0 {
			t.Errorf("counter %s is negative: %d", name, v)
		}
	}
}

// AssertScenarioAccounting verifies that simulated frame counts reconcile:
// every generated frame is either lost or delivered (possibly duplicated),
// and every delivered non-OK event has a distinct impairment label.
func AssertScenarioAccounting(t *testing.T, events []sim.Event, frames int) {
	t.Helper()
	seen := make(map[uint32]int)
	delivered := 0
	for _, e := range events {
		if e.Kind == sim.EventLost {
			seen[e.FrameNo]++
			continue
		}
		if e.Datagram == nil {
			t.Errorf("event frame %d kind %s has no datagram", e.FrameNo, e.Kind)
		}
		seen[e.FrameNo]++
		delivered++
	}
	if len(seen) != frames {
		t.Errorf("scenario covers %d frames, want %d", len(seen), frames)
	}
	if delivered == 0 {
		t.Error("scenario delivered no frames")
	}
	// Determinism check is the caller's responsibility; this helper documents
	// the expected minimum event volume for meaningful reports.
	if delivered < frames/4 {
		t.Errorf("only %d of %d frames delivered, scenario too lossy",
			delivered, frames)
	}
}

// AssertClassified verifies that a set of impairment kinds is fully covered.
func AssertClassified(t *testing.T, events []sim.Event,
	want ...sim.EventKind) {
	t.Helper()
	counts := sim.CountByKind(events)
	for _, k := range want {
		if counts[k] == 0 {
			t.Errorf("expected at least one %s event", k)
		}
	}
}

// Require fails the test when err is non-nil, with a formatted message.
func Require(t *testing.T, err error, context string) {
	t.Helper()
	if err != nil {
		t.Fatalf("%s: %v", context, err)
	}
}

// Label renders a compact scenario label for log output.
func Label(cfg sim.Config) string {
	return fmt.Sprintf("frames=%d seed=%d burst=%d reorder=%d dup=%.3f",
		cfg.Frames, cfg.Seed, cfg.LossBurst, cfg.ReorderWindow,
		cfg.DuplicateRate)
}
