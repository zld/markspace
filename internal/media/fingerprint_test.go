package media

import (
	"testing"

	"github.com/archive/mediakernel/internal/manifest"
	"github.com/archive/mediakernel/internal/timeline"
)

func TestFingerprintDeterministicAndLengthSensitive(t *testing.T) {
	a := pcm(100, 200, -300, 400)
	h1, err := Fingerprint(a, FingerprintConfig{Tolerance: 0})
	if err != nil {
		t.Fatal(err)
	}
	h2, _ := Fingerprint(a, FingerprintConfig{Tolerance: 0})
	if h1 != h2 {
		t.Fatal("identical content must hash identically")
	}
	// Different sample count must never collide even with a shared prefix.
	longer := pcm(100, 200, -300, 400, 500)
	h3, _ := Fingerprint(longer, FingerprintConfig{Tolerance: 0})
	if h1 == h3 {
		t.Fatal("different-length content collided")
	}
	different := pcm(100, 200, -300, 401)
	h4, _ := Fingerprint(different, FingerprintConfig{Tolerance: 0})
	if h1 == h4 {
		t.Fatal("bit-exact mode must distinguish a one-sample change")
	}
}

func TestFingerprintTolerance(t *testing.T) {
	base := pcm(1000, -2000, 3000, -4000)
	// Small amplitude jitter within one quantization bin at tolerance 1 (step 4).
	jitter := pcm(1001, -1999, 3002, -4001)
	exact, _ := Fingerprint(base, FingerprintConfig{Tolerance: 0})
	jitterExact, _ := Fingerprint(jitter, FingerprintConfig{Tolerance: 0})
	if exact == jitterExact {
		t.Fatal("exact mode should distinguish jitter")
	}
	tolBase, _ := Fingerprint(base, FingerprintConfig{Tolerance: 1})
	tolJitter, _ := Fingerprint(jitter, FingerprintConfig{Tolerance: 1})
	if tolBase != tolJitter {
		t.Fatal("within-tolerance jitter must hash equal")
	}
	// A large change outside the bin still differs under tolerance.
	big := pcm(1000, -2000, 3000, 8000)
	tolBig, _ := Fingerprint(big, FingerprintConfig{Tolerance: 1})
	if tolBase == tolBig {
		t.Fatal("large amplitude change must not match under tolerance")
	}
}

func TestFingerprintConfigValidation(t *testing.T) {
	if _, err := Fingerprint(pcm(1), FingerprintConfig{Tolerance: MaxTolerance + 1}); timeline.Classify(err) != timeline.KindInvalidInput {
		t.Fatalf("bad tolerance: %v", err)
	}
}

func TestDedupKeepsSourceMapping(t *testing.T) {
	segs := []SegmentContent{
		{ID: "s0", StartUS: 0, EndUS: 100_000, PCM: pcm(10, 20, 30)},
		{ID: "s1", StartUS: 200_000, EndUS: 300_000, PCM: pcm(99, 88, 77)},
		{ID: "s2", StartUS: 400_000, EndUS: 500_000, PCM: pcm(10, 20, 30)}, // repeats s0
	}
	records, err := FingerprintAndDedup(segs, nil, FingerprintConfig{Tolerance: 0})
	if err != nil {
		t.Fatal(err)
	}
	if len(records) != 2 {
		t.Fatalf("records = %d, want 2 distinct fingerprints", len(records))
	}
	// Find the duplicated hash and assert both source ranges are retained.
	var dup *manifest.FingerprintRecord
	for i := range records {
		if records[i].Duplicate {
			dup = &records[i]
		}
	}
	if dup == nil {
		t.Fatalf("expected one duplicate record: %+v", records)
	}
	if len(dup.SourceRanges) != 2 {
		t.Fatalf("source ranges = %d, want 2", len(dup.SourceRanges))
	}
	if dup.SourceRanges[0].StartUS != 0 || dup.SourceRanges[1].StartUS != 400_000 {
		t.Fatalf("provenance lost: %+v", dup.SourceRanges)
	}
	// Output sorted by start time.
	for i := 1; i < len(records); i++ {
		if records[i].StartUS < records[i-1].StartUS {
			t.Fatal("records not sorted")
		}
	}
}

// TestDedupNeverMergesAcrossGap is the required invariant: identical bytes on
// both sides of a confirmed loss gap must not be marked as a mergeable
// duplicate, because doing so would fabricate continuity.
func TestDedupNeverMergesAcrossGap(t *testing.T) {
	segs := []SegmentContent{
		{ID: "s0", StartUS: 0, EndUS: 100_000, PCM: pcm(10, 20, 30)},
		{ID: "s1", StartUS: 300_000, EndUS: 400_000, PCM: pcm(10, 20, 30)},
	}
	gaps := []manifest.LossGap{{StartUS: 100_000, EndUS: 300_000, MissingCount: 14}}
	records, err := FingerprintAndDedup(segs, gaps, FingerprintConfig{Tolerance: 0})
	if err != nil {
		t.Fatal(err)
	}
	if len(records) != 1 {
		t.Fatalf("records = %d, want 1 shared-hash record", len(records))
	}
	if records[0].Duplicate {
		t.Fatal("segments across a gap must not be marked duplicate/mergeable")
	}
	if len(records[0].SourceRanges) != 2 {
		t.Fatal("both source ranges must still be preserved")
	}
}

func TestDedupRejectsInvalidRange(t *testing.T) {
	bad := []SegmentContent{{ID: "x", StartUS: 100, EndUS: 50, PCM: pcm(1)}}
	if _, err := FingerprintAndDedup(bad, nil, FingerprintConfig{}); timeline.Classify(err) != timeline.KindInvalidInput {
		t.Fatalf("invalid range: %v", err)
	}
}
