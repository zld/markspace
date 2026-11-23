package pipeline_test

import (
	"context"
	"testing"
	"time"

	"mediad/internal/fanout"
	"mediad/internal/pipeline"
	"mediad/internal/testkit"
)

// demoOptions mirrors the CLI's normal-path demo invocation.
func demoOptions(lossEvery int) pipeline.Options {
	opts := pipeline.Options{SlowConsumer: 120 * time.Millisecond}
	if lossEvery > 0 {
		opts.LayerTransport = map[string]fanout.Transport{
			fanout.LadderNames[0]: {DropEvery: lossEvery},
			fanout.LadderNames[1]: {DropEvery: lossEvery, SwapEvery: lossEvery + 6},
			fanout.LadderNames[2]: {DropEvery: lossEvery},
		}
	}
	return opts
}

func runFixture(t *testing.T, fault string, opts pipeline.Options) *pipeline.Output {
	t.Helper()
	fixture, err := testkit.BuildFaulted(fault, 42)
	if err != nil {
		t.Fatalf("build fixture %s: %v", fault, err)
	}
	out, err := pipeline.Run(context.Background(), fixture.Data, opts)
	if err != nil {
		t.Fatalf("run %s: %v", fault, err)
	}
	return out
}

func TestNormalPathMeetsAcceptanceTargets(t *testing.T) {
	out := runFixture(t, testkit.FaultNone, demoOptions(17))
	s := out.Summary

	if s.SyncErrorMs > 20 {
		t.Errorf("sync_error_ms = %.3f, target <= 20", s.SyncErrorMs)
	}
	if s.PacketRecoveryRate < 0.90 {
		t.Errorf("packet_recovery_rate = %.4f, target >= 0.90", s.PacketRecoveryRate)
	}
	if s.P95LatencyMs > 250 {
		t.Errorf("p95_latency_ms = %.3f, target <= 250", s.P95LatencyMs)
	}
	if s.IndexEntries < 10 {
		t.Errorf("index_entries = %d, target >= 10", s.IndexEntries)
	}
	if s.BoundaryMismatch != 0 {
		t.Errorf("boundary_mismatch = %d, target 0", s.BoundaryMismatch)
	}
	if out.Damage != nil {
		t.Errorf("healthy input reported damage: %v", out.Damage)
	}
	if !s.TimestampMonotonic {
		t.Error("timestamps not monotonic")
	}
	if !s.IndexKeyframesOnly {
		t.Error("index does not point only at keyframes")
	}
	if !s.LayersConsistent {
		t.Error("layers inconsistent")
	}
	if s.MP4CrossCheckEntries != s.IndexEntries {
		t.Errorf("mp4 cross-check saw %d entries, index has %d",
			s.MP4CrossCheckEntries, s.IndexEntries)
	}
	if s.SummaryHash == "" {
		t.Error("empty summary hash")
	}
}

func TestSlowConsumerOnlyStallsItsLayer(t *testing.T) {
	out := runFixture(t, testkit.FaultNone, demoOptions(0))
	slow, fast1, fast2 := out.LadderStats[0], out.LadderStats[1], out.LadderStats[2]
	// Layer order is 360p, 720p, 1080p, and only 720p is slowed.
	if slow.Name != "360p" || fast1.Name != "720p" || fast2.Name != "1080p" {
		t.Fatalf("ladder order = %s,%s,%s", slow.Name, fast1.Name, fast2.Name)
	}
	if fast1.BackpressureNs == 0 {
		t.Fatal("slow layer recorded no backpressure")
	}
	for _, l := range []fanout.LayerStats{slow, fast2} {
		if l.PeakBytes > fanout.BufferBytes {
			t.Errorf("%s peak %d exceeds budget", l.Name, l.PeakBytes)
		}
	}
	// All three layers receive every block; the slow one delays the
	// publisher but must not lose data downstream.
	for _, l := range out.LadderStats {
		if l.Offered != out.Summary.Blocks {
			t.Errorf("%s offered %d of %d", l.Name, l.Offered, out.Summary.Blocks)
		}
		if l.Dropped != 0 {
			t.Errorf("%s dropped %d blocks with no transport loss configured", l.Name, l.Dropped)
		}
	}
}
