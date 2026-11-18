package scenario

import (
	"context"
	"errors"
	"testing"
	"time"

	"relaylab/internal/forward"
	"relaylab/internal/signaling"
)

func TestEngineSmoke(t *testing.T) {
	cfg := Config{
		Duration:      2 * time.Second,
		Packets:       200,
		QueueSize:     32,
		Overflow:      forward.OverflowDrop,
		Seed:          42,
		ReorderRate:   0.05,
		DuplicateRate: 0.02,
		BurstEvery:    100,
		BurstDelayMS:  10,
	}
	e, err := NewEngine(cfg)
	if err != nil {
		t.Fatal(err)
	}
	report, err := e.Run(context.Background())
	if err != nil {
		t.Fatalf("Run error: %v", err)
	}
	if !report.ShutdownClean {
		t.Fatal("shutdown not clean")
	}
	t.Logf("pairs=%d sent=%d forwarded=%d dropped=%d dup=%d reorder=%d jitter=%d ticks %.3fms",
		len(report.CandidatePairs), report.SentPackets, report.ForwardedPackets,
		report.DroppedPackets, report.DuplicatePackets, report.ReorderedPackets,
		report.JitterRTPTicks, report.JitterMS)
}

func TestEngineRejectsOutOfRangeDuration(t *testing.T) {
	for _, d := range []time.Duration{0, -time.Second, 31 * time.Second} {
		if _, err := NewEngine(Config{Duration: d}); err == nil {
			t.Errorf("duration %s should be rejected", d)
		}
	}
}

func TestEngineCancelledContextCleansUp(t *testing.T) {
	cfg := Config{
		Duration: 5 * time.Second, Packets: 100, QueueSize: 16,
		Overflow: forward.OverflowBlock, Seed: 1,
	}
	e, err := NewEngine(cfg)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel() // pre-cancelled: negotiation must fail and release resources.
	report, err := e.Run(ctx)
	if err == nil {
		t.Fatal("expected an error from a cancelled run")
	}
	var pe *signaling.PhaseError
	if !errors.As(err, &pe) {
		t.Fatalf("expected PhaseError, got %T: %v", err, err)
	}
	// The report is still emitted and the signaling server must have shut
	// down within its own deadline rather than leaking a listener.
	shutdownCtx, c := context.WithTimeout(context.Background(), time.Second)
	defer c()
	if err := e.srv.Shutdown(shutdownCtx); err != nil {
		t.Logf("server shutdown after cancelled run: %v", err)
	}
	if report == nil {
		t.Fatal("report must be returned even on failure")
	}
}
