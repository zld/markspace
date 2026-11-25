package rtptrace

import (
	"reflect"
	"testing"

	"tracecheck/internal/clock"
	"tracecheck/internal/probe"
)

func sampleSet(n int) []probe.RecoveredSample {
	out := make([]probe.RecoveredSample, n)
	for i := range out {
		out[i] = probe.RecoveredSample{
			TrackID: 1, Index: uint32(i), Offset: clock.Offset(1000 + i*32),
			Start: clock.Tick(i * 900), Duration: 900, Sync: i%4 == 0,
		}
	}
	return out
}

func TestFromTimelinePreservesOrderAndOffsets(t *testing.T) {
	pkts := FromTimeline(sampleSet(6), TimelineOptions{SSRC: 42, StartSequence: 100})
	if len(pkts) != 6 {
		t.Fatalf("packet count = %d", len(pkts))
	}
	for i, p := range pkts {
		if p.Sequence != 100+uint16(i) || p.Timestamp != uint32(i*900) || p.SSRC != 42 {
			t.Fatalf("header wrong at %d: %+v", i, p)
		}
		if int64(p.SourceOffset) != int64(1000+i*32) {
			t.Fatalf("source offset changed at %d: %d", i, p.SourceOffset)
		}
	}
}

func TestMarshalRoundTrip(t *testing.T) {
	pkts := FromTimeline(sampleSet(3), TimelineOptions{SSRC: 7})
	for _, p := range pkts {
		got, err := ParseRoundTrip(p)
		if err != nil {
			t.Fatalf("round trip: %v", err)
		}
		if got.SequenceNumber != p.Sequence || got.Timestamp != p.Timestamp ||
			got.SSRC != p.SSRC {
			t.Fatalf("wire fields lost: %+v", got.Header)
		}
	}
}

func TestApplyIsDeterministicAndCountsImpairments(t *testing.T) {
	pkts := FromTimeline(sampleSet(12), TimelineOptions{SSRC: 1})
	plan := Plan{
		Seed:        99,
		LossRate:    0.25,
		DuplicateAt: []int{2},
		ReorderAt:   []int{5},
	}
	first := Apply(pkts, plan)
	second := Apply(pkts, plan)
	if len(first.Packets) != len(second.Packets) {
		t.Fatal("rerun changed packet count")
	}
	for i := range first.Packets {
		if !reflect.DeepEqual(first.Packets[i], second.Packets[i]) {
			t.Fatalf("nondeterministic output at %d", i)
		}
	}
	if first.Stats.Lost == 0 {
		t.Fatal("expected losses at 0.25 rate")
	}
	if first.Stats.Duplicated != 1 {
		t.Fatalf("duplicated = %d, want 1", first.Stats.Duplicated)
	}
	if first.Stats.Delivered != len(first.Packets) {
		t.Fatal("delivered count inconsistent")
	}

	// Reordering is asserted without loss so the targeted pair always exists.
	reorderTrace := Apply(pkts, Plan{Seed: 1, ReorderAt: []int{5}})
	if reorderTrace.Stats.Reordered != 1 {
		t.Fatalf("reordered = %d, want 1", reorderTrace.Stats.Reordered)
	}
}

func TestApplyKeepsSourceOffsetsImmutable(t *testing.T) {
	pkts := FromTimeline(sampleSet(8), TimelineOptions{SSRC: 1})
	plan := Plan{Seed: 5, LossRate: 0.3, DuplicateAt: []int{1}, ReorderAt: []int{3}}
	trace := Apply(pkts, plan)
	original := map[clock.Offset]bool{}
	for _, p := range pkts {
		original[p.SourceOffset] = true
	}
	for _, p := range trace.Packets {
		if !original[p.SourceOffset] {
			t.Fatalf("packet carries unknown offset %d", p.SourceOffset)
		}
	}
}

func TestMapBackToTimelineSortsByOffset(t *testing.T) {
	pkts := FromTimeline(sampleSet(8), TimelineOptions{SSRC: 1})
	trace := Apply(pkts, Plan{Seed: 3, ReorderAt: []int{1, 4}})
	mapped := MapBackToTimeline(trace)
	for i := 1; i < len(mapped); i++ {
		if mapped[i].SourceOffset < mapped[i-1].SourceOffset {
			t.Fatal("map-back not ordered by source offset")
		}
	}
}

func TestNoImpairmentDeliversEverything(t *testing.T) {
	pkts := FromTimeline(sampleSet(5), TimelineOptions{SSRC: 1})
	trace := Apply(pkts, Plan{Seed: 1})
	if trace.Stats != (Stats{Delivered: 5}) {
		t.Fatalf("unexpected stats: %+v", trace.Stats)
	}
}
