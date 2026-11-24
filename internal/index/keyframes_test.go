package index

import (
	"math"
	"testing"
)

func TestBuildVFREntriesAndIntervals(t *testing.T) {
	idx := &Index{}
	// VFR: keyframes at 0.0, 0.04 (40 ms), 0.13 (90 ms gap).
	samples := []SampleView{
		{SampleIndex: 0, Keyframe: true, ByteOffset: 100, TimeSeconds: 0.0},
		{SampleIndex: 1, Keyframe: false, ByteOffset: 140, TimeSeconds: 0.02},
		{SampleIndex: 2, Keyframe: true, ByteOffset: 180, TimeSeconds: 0.04},
	}
	last := Build(idx, "seg0.m4s", 0, samples, 0, false)
	if math.Abs(last-0.04) > 1e-12 {
		t.Fatalf("returned last key time = %v", last)
	}
	more := []SampleView{
		{SampleIndex: 0, Keyframe: false, ByteOffset: 300, TimeSeconds: 0.08},
		{SampleIndex: 1, Keyframe: true, ByteOffset: 340, TimeSeconds: 0.13},
	}
	Build(idx, "seg1.m4s", 1, more, last, true)

	entries := idx.Snapshot()
	if len(entries) != 3 {
		t.Fatalf("entries = %d, want 3", len(entries))
	}
	if entries[0].IntervalSeconds != 0 {
		t.Fatal("first keyframe must have zero interval")
	}
	if math.Abs(entries[1].IntervalSeconds-0.04) > 1e-12 {
		t.Fatalf("interval 1 = %v", entries[1].IntervalSeconds)
	}
	// Cross-segment VFR gap measured from real times, not a fixed rate.
	if math.Abs(entries[2].IntervalSeconds-0.09) > 1e-12 {
		t.Fatalf("interval 2 = %v, want 0.09", entries[2].IntervalSeconds)
	}
}

func TestLookupReturnsOriginalLocation(t *testing.T) {
	idx := &Index{}
	idx.Add(Entry{Segment: "seg2.m4s", MediaSeq: 2, SampleIndex: 5,
		ByteOffset: 777, TimeSeconds: 1.0})
	got, ok := idx.Lookup("seg2.m4s", 5)
	if !ok || got.ByteOffset != 777 || got.MediaSeq != 2 {
		t.Fatalf("lookup wrong: %+v ok=%v", got, ok)
	}
	if _, ok := idx.Lookup("seg2.m4s", 6); ok {
		t.Fatal("missing sample must not hit")
	}
	if _, ok := idx.LookupAtOrBefore(0.9); ok {
		t.Fatal("no keyframe should exist before 1.0")
	}
	before, ok := idx.LookupAtOrBefore(1.5)
	if !ok || before.SampleIndex != 5 {
		t.Fatalf("at-or-before lookup wrong: %+v", before)
	}
}

func TestQuarantineAndHitRate(t *testing.T) {
	idx := &Index{}
	for s, seg := range []string{"a.m4s", "b.m4s"} {
		for i := 0; i < 2; i++ {
			idx.Add(Entry{Segment: seg, SampleIndex: i,
				TimeSeconds: float64(s*2 + i)})
		}
	}
	queries := []Query{
		{Segment: "a.m4s", SampleIndex: 0},
		{Segment: "a.m4s", SampleIndex: 1},
		{Segment: "b.m4s", SampleIndex: 0},
		{Segment: "b.m4s", SampleIndex: 1},
	}
	if rate := idx.HitRate(queries); rate != 1.0 {
		t.Fatalf("initial hit rate = %v", rate)
	}
	n := idx.Quarantine("b.m4s")
	if n != 2 || idx.Available() != 2 {
		t.Fatalf("quarantine isolated %d, available %d", n, idx.Available())
	}
	if _, ok := idx.Lookup("b.m4s", 0); ok {
		t.Fatal("quarantined entry must not resolve")
	}
	if rate := idx.HitRate(queries); math.Abs(rate-0.5) > 1e-12 {
		t.Fatalf("post-damage hit rate = %v, want 0.5", rate)
	}
}
