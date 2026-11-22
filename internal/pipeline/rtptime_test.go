package pipeline

import (
	"math"
	"testing"

	"mediaarchive/internal/stream"
)

func TestRTPMediaTimesUsesTimestampsNotIndices(t *testing.T) {
	// Uneven capture: packet 0 at t=0, packet 1 after a 200 ms burst,
	// packet 2 back on the 33 ms cadence. Index interpolation would smear
	// these into equal steps.
	pkts := []stream.Packet{
		{Seq: 1, Timestamp: 0},
		{Seq: 2, Timestamp: 90000 / 5},
		{Seq: 3, Timestamp: 90000/5 + 3000},
	}
	got := RTPMediaTimes(pkts)
	if math.Abs(got[1]-200) > 1e-9 || math.Abs(got[2]-233.3333) > 0.01 {
		t.Fatalf("times = %v", got)
	}
}

func TestRTPMediaTimesFolds32BitWrap(t *testing.T) {
	pkts := []stream.Packet{
		{Seq: 65534, Timestamp: 0xFFFFFF00},
		{Seq: 65535, Timestamp: 0x00000D10}, // +3600 ticks across the 2^32 wrap
	}
	got := RTPMediaTimes(pkts)
	if math.Abs(got[1]-40) > 1e-6 {
		t.Fatalf("wrap delta = %v ms, want 40", got[1])
	}
	if got[0] != 0 {
		t.Fatalf("first must be zero-based: %v", got[0])
	}
}

func TestManifestValidationRejectsDanglingReferences(t *testing.T) {
	base := &Manifest{
		AssetIDs:    []string{"a"},
		SourceSizes: map[string]int64{"a": 1000},
		Rows:        []Row{{Asset: "a", StartMs: 0, StartUs: 0}},
	}
	if err := ValidateManifest(base); err != nil {
		t.Fatalf("valid manifest rejected: %v", err)
	}
	bad := *base
	bad.Rows = []Row{{Asset: "ghost", StartMs: 0}}
	if err := ValidateManifest(&bad); err == nil {
		t.Fatal("dangling row reference accepted")
	}
	bad2 := *base
	bad2.Segments = []Segment{{Source: "a", Code: "mp4", StartMs: 0, EndMs: 10, StartOffset: 0, EndOffset: 1000}}
	if err := ValidateManifest(&bad2); err == nil {
		t.Fatal("segment beyond source extent accepted")
	}
	bad3 := *base
	bad3.Segments = []Segment{{Source: "ghost", Code: "mp4"}}
	if err := ValidateManifest(&bad3); err == nil {
		t.Fatal("dangling segment reference accepted")
	}
}
