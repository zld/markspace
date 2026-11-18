package scenario

import (
	"context"
	"testing"
	"time"

	"relaylab/internal/forward"
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
