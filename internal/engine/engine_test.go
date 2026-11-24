package engine

import (
	"context"
	"testing"

	"github.com/example/tsrepair/internal/fixture"
)

func TestCleanStreamFullRoundtrip(t *testing.T) {
	stream := fixture.NewGenerator(7).Generate(40, nil)
	e := New(Config{QueueCapacity: 2, SegmentPackets: 10}, stream)
	m, err := e.Run(context.Background())
	if err != nil {
		t.Fatalf("clean run: %v", err)
	}
	if m.ParseRoundtripPct != 100 {
		t.Fatalf("parse_roundtrip=%.2f want 100", m.ParseRoundtripPct)
	}
	if m.KeyframeHitRatePct != 100 {
		t.Fatalf("keyframe_hit_rate=%.2f want 100", m.KeyframeHitRatePct)
	}
	if m.HashMatchPct != 100 {
		t.Fatalf("segment_hash_match=%.2f want 100", m.HashMatchPct)
	}
	if m.TruncatedBytes != 0 || m.MaxQueueDepth > 2 {
		t.Fatalf("clean invariants wrong: %+v", m)
	}
}

func TestFaultStreamRecoverMetrics(t *testing.T) {
	stream := fixture.NewGenerator(7).Generate(60, fixture.AllFaults)
	e := New(Config{QueueCapacity: 1, SegmentPackets: 10, Recover: true}, stream)
	m, err := e.Run(context.Background())
	if err != nil {
		t.Fatalf("recover mode must swallow recoverable faults: %v", err)
	}
	if m.RepairedWraps == 0 {
		t.Fatal("expected at least one repaired PCR wrap")
	}
	if m.IgnoredDuplicates == 0 {
		t.Fatal("expected an ignored duplicate")
	}
	if m.ContinuityGaps == 0 {
		t.Fatal("expected a continuity gap")
	}
	if m.PSIVersions < 2 {
		t.Fatalf("psi boundary count=%d want >=2", m.PSIVersions)
	}
	if m.AudioErrors == 0 {
		t.Fatal("expected audio metadata errors")
	}
	if m.TruncatedBytes == 0 {
		t.Fatal("truncated tail must be counted")
	}
	if m.MaxQueueDepth > 1 {
		t.Fatalf("queue depth %d exceeds capacity 1", m.MaxQueueDepth)
	}
	// The confirmable portion must still be fully verifiable.
	if m.HashMatchPct != 100 {
		t.Fatalf("hash match=%.2f want 100 on confirmed segments", m.HashMatchPct)
	}
	if m.KeyframeHitRatePct != 100 {
		t.Fatalf("keyframe hit=%.2f want 100", m.KeyframeHitRatePct)
	}
}

func TestFaultStreamWithoutRecoveryFails(t *testing.T) {
	stream := fixture.NewGenerator(7).Generate(40,
		[]string{fixture.FaultAudioLayout})
	e := New(Config{QueueCapacity: 2, SegmentPackets: 10, Recover: false}, stream)
	_, err := e.Run(context.Background())
	if err == nil {
		t.Fatal("non-recover run must surface the first classified fault")
	}
}

func TestRerunDeterministic(t *testing.T) {
	run := func() *Metrics {
		s := fixture.NewGenerator(7).Generate(50, fixture.AllFaults)
		e := New(Config{QueueCapacity: 1, SegmentPackets: 8, Recover: true}, s)
		m, err := e.Run(context.Background())
		if err != nil {
			t.Fatal(err)
		}
		return m
	}
	a, b := run(), run()
	if *a != *b {
		t.Fatalf("rerun metrics differ:\n%+v\n%+v", a, b)
	}
}
