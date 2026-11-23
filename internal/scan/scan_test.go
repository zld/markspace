package scan

import (
	"bytes"
	"errors"
	"testing"
	"time"

	"mediainspect/internal/media"
)

func buildIndex(t *testing.T, cfg GenConfig) *Index {
	t.Helper()
	idx, err := BuildIndex(NewReader(NewGenerator(cfg)))
	if err != nil {
		t.Fatalf("BuildIndex: %v", err)
	}
	return idx
}

func TestBuildIndexBasics(t *testing.T) {
	idx := buildIndex(t, GenConfig{Seed: 42, Frames: 300, TargetKbps: 1200})
	if idx.Frames != 300 {
		t.Fatalf("frames = %d, want 300", idx.Frames)
	}
	if len(idx.Entries) != 300 {
		t.Fatalf("entries = %d", len(idx.Entries))
	}
	if got := len(idx.KeyframePositions); got != 10 {
		t.Fatalf("keyframes = %d, want 10", got)
	}
	if idx.Digest == "" || idx.StructureSummary == "" {
		t.Fatal("digest and structure summary must be recorded")
	}
	if idx.ChecksumAggregate == 0 {
		t.Fatal("checksum aggregate must be recorded")
	}
}

func TestDigestDeterministic(t *testing.T) {
	a := buildIndex(t, GenConfig{Seed: 42, Frames: 300})
	b := buildIndex(t, GenConfig{Seed: 42, Frames: 300})
	c := buildIndex(t, GenConfig{Seed: 43, Frames: 300})
	if a.Digest != b.Digest {
		t.Fatal("same seed must yield same digest")
	}
	if a.Digest == c.Digest {
		t.Fatal("different seed must yield different digest")
	}
}

func TestIndexRoundTrip(t *testing.T) {
	idx := buildIndex(t, GenConfig{Seed: 7, Frames: 120, LossBurst: 2})
	var buf bytes.Buffer
	if err := SaveIndex(idx, &buf); err != nil {
		t.Fatalf("SaveIndex: %v", err)
	}
	got, err := LoadIndex(&buf)
	if err != nil {
		t.Fatalf("LoadIndex: %v", err)
	}
	if got.Digest != idx.Digest || got.Frames != idx.Frames ||
		got.ChecksumAggregate != idx.ChecksumAggregate ||
		len(got.Entries) != len(idx.Entries) ||
		got.StructureSummary != idx.StructureSummary {
		t.Fatal("round trip mismatch")
	}
}

func TestLoadIndexTruncated(t *testing.T) {
	idx := buildIndex(t, GenConfig{Seed: 7, Frames: 120})
	var buf bytes.Buffer
	if err := SaveIndex(idx, &buf); err != nil {
		t.Fatalf("SaveIndex: %v", err)
	}
	data := buf.Bytes()
	for _, n := range []int{0, 3, len(data) / 2, len(data) - 1} {
		if _, err := LoadIndex(bytes.NewReader(data[:n])); !errors.Is(err, media.ErrIndexTruncated) {
			t.Fatalf("truncated to %d: err = %v, want ErrIndexTruncated", n, err)
		}
	}
	// Bit flip must also be detected via the integrity checksum.
	data[len(data)/2] ^= 0xff
	if _, err := LoadIndex(bytes.NewReader(data)); !errors.Is(err, media.ErrIndexTruncated) {
		t.Fatalf("corrupt: err = %v, want ErrIndexTruncated", err)
	}
}

func TestStaleIndexDetection(t *testing.T) {
	fresh := buildIndex(t, GenConfig{Seed: 42, Frames: 200})
	stale := buildIndex(t, GenConfig{Seed: 1, Frames: 200})
	if fresh.Digest == stale.Digest {
		t.Fatal("fixture broken: digests must differ")
	}
	if err := CheckIndexFresh(stale, fresh.Digest); !errors.Is(err, media.ErrStaleIndex) {
		t.Fatalf("err = %v, want ErrStaleIndex", err)
	}
	if err := CheckIndexFresh(fresh, fresh.Digest); err != nil {
		t.Fatalf("fresh index rejected: %v", err)
	}
}

// CheckIndexFresh lives in index.go; exercised above.

func TestTimestampWrapBoundary(t *testing.T) {
	idx := buildIndex(t, GenConfig{Seed: 5, Frames: 100, Wrap: true})
	if idx.Frames != 100 {
		t.Fatalf("frames = %d", idx.Frames)
	}
	// Raw timestamps wrap past 2^32; the recorded range must reflect the
	// wrapped wire values, not a monotonic interpretation.
	if idx.MinTimestamp >= idx.MaxTimestamp {
		t.Fatalf("expected wrapped range, got min=%d max=%d", idx.MinTimestamp, idx.MaxTimestamp)
	}
}

func TestScanPerformance100k(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping 100k-frame scan in short mode")
	}
	start := time.Now()
	idx := buildIndex(t, GenConfig{Seed: 99, Frames: 100000, TargetKbps: 1200})
	elapsed := time.Since(start)
	if idx.Frames != 100000 {
		t.Fatalf("frames = %d", idx.Frames)
	}
	if elapsed > 2*time.Second {
		t.Fatalf("scan of 100k frames took %v, want < 2s", elapsed)
	}
	t.Logf("100k-frame scan: %v", elapsed)
}
