package pipeline

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"example.com/media-kernel/provenance"
	"example.com/media-kernel/sample"
	"example.com/media-kernel/state"
)

func TestAnalyzeFixtureRecoveryAndFragmentWrites(t *testing.T) {
	f, err := sample.Generate()
	if err != nil {
		t.Fatal(err)
	}
	res, err := AnalyzeFixture(context.Background(), f, Options{MaxMemoryBytes: 8 << 20})
	if err != nil {
		t.Fatal(err)
	}
	if res.DuplicatePackets != 1 || len(res.MissingSequences) != 1 || res.MissingSequences[0] != 65538 {
		t.Fatalf("sequence summary wrong: dup=%d missing=%v", res.DuplicatePackets, res.MissingSequences)
	}
	if res.NormalMP4.RoundTrip.Rate != 1 || res.NormalMP4.Index.HitRate() != 1 {
		t.Fatalf("normal fixture rates wrong: rt=%v hit=%v",
			res.NormalMP4.RoundTrip.Rate, res.NormalMP4.Index.HitRate())
	}
	if len(res.Segments) != 4 {
		t.Fatalf("segments=%d want 4", len(res.Segments))
	}
	if len(res.Damage.Regions) < 3 {
		t.Fatalf("damage regions=%d want at least 3", len(res.Damage.Regions))
	}
	if res.PeakTrackedBytes <= 0 || res.PeakTrackedBytes > res.MaxMemoryBytes {
		t.Fatalf("memory peak=%d cap=%d", res.PeakTrackedBytes, res.MaxMemoryBytes)
	}

	paramsHash := provenance.HashBytes([]byte("test-params"))
	store := &state.Store{Path: filepath.Join(t.TempDir(), "nested", "cp.json")}
	rec, err := SimulateInterruptAndResume(store, res, paramsHash, 2)
	if err != nil {
		t.Fatal(err)
	}
	if rec.ResumedFrom != 2 || len(rec.BeforeHashes) != 2 {
		t.Fatalf("recovery=%+v", rec)
	}
	for i, before := range rec.BeforeHashes {
		if rec.AfterHashes[i] != before {
			t.Fatalf("segment %d changed across recovery", i)
		}
	}
	if err := VerifyIdempotentRetry(store, res, paramsHash); err != nil {
		t.Fatal(err)
	}

	segmentDir := filepath.Join(t.TempDir(), "segments")
	paths, err := WriteSegments(segmentDir, f.NormalMP4, res.Segments)
	if err != nil {
		t.Fatal(err)
	}
	if len(paths) != len(res.Segments) {
		t.Fatalf("paths=%d segments=%d", len(paths), len(res.Segments))
	}
	for i, path := range paths {
		b, err := os.ReadFile(path)
		if err != nil {
			t.Fatal(err)
		}
		if got := provenance.HashBytes(b); got != res.Segments[i].SHA256 {
			t.Fatalf("written fragment %d hash mismatch", i)
		}
	}
}
