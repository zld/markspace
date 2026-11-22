package media

import (
	"testing"

	"github.com/archive/mediakernel/internal/testfixture"
	"github.com/archive/mediakernel/internal/timeline"
)

func TestScanBuildsUnifiedAxisIndex(t *testing.T) {
	const timescale = uint32(48000)
	spec := testfixture.MP4Spec{
		Seed: 73, Timescale: timescale, SampleCount: 12, SampleDelta: 960,
		SampleSize: 200, KeyframeEvery: 4, StartOffset: 4800, // first sample at 100 ms
	}
	data, err := testfixture.GenerateMP4(spec)
	if err != nil {
		t.Fatal(err)
	}
	idx, err := ScanBytes(data)
	if err != nil {
		t.Fatalf("ScanBytes: %v", err)
	}
	if idx.Digest == "" || idx.Size != int64(len(data)) {
		t.Fatal("digest/size not populated")
	}
	if len(idx.Tracks) != 1 {
		t.Fatalf("tracks = %d", len(idx.Tracks))
	}
	tr := idx.Tracks[0]
	if len(tr.Samples) != 13 || len(tr.Keyframes) != 3 { // 1 offset + 12 real
		t.Fatalf("samples=%d keyframes=%v", len(tr.Samples), tr.Keyframes)
	}
	// The leading zero-size placeholder spans the non-zero start offset.
	if tr.Samples[0].Size != 0 || tr.Samples[0].StartUS != 0 || tr.Samples[0].DurationUS != 100_000 {
		t.Fatalf("offset placeholder wrong: %+v", tr.Samples[0])
	}
	// First real sample begins exactly at the non-zero start time 100 ms.
	first := tr.Samples[1]
	if first.StartUS != 100_000 || first.Size != 200 {
		t.Fatalf("first real sample = %+v, want start 100000 size 200", first)
	}
	// Real sample cadence: 960/48000 s = 20000 us.
	if tr.Samples[2].StartUS != 120_000 || tr.Samples[2].DurationUS != 20_000 {
		t.Fatalf("sample 2 start=%d dur=%d", tr.Samples[2].StartUS, tr.Samples[2].DurationUS)
	}
	// Keyframes (0-based) are the real samples 1,5,9.
	if !tr.Samples[1].IsKeyframe || tr.Samples[2].IsKeyframe {
		t.Fatal("keyframe flags wrong")
	}
}

func TestScanDigestChangesWithContent(t *testing.T) {
	base := testfixture.MP4Spec{Seed: 73, Timescale: 48000, SampleCount: 6, SampleDelta: 960, SampleSize: 100, KeyframeEvery: 2}
	a, _ := testfixture.GenerateMP4(base)
	changed := base
	changed.Seed = 74
	b, _ := testfixture.GenerateMP4(changed)
	ia, err := ScanBytes(a)
	if err != nil {
		t.Fatal(err)
	}
	ib, err := ScanBytes(b)
	if err != nil {
		t.Fatal(err)
	}
	if ia.Digest == ib.Digest {
		t.Fatal("different content must produce different digests")
	}
	// Identical input rescans to the identical digest: the basis for index
	// invalidation and repeat-run stability.
	ia2, err := ScanBytes(a)
	if err != nil {
		t.Fatal(err)
	}
	if ia2.Digest != ia.Digest {
		t.Fatal("rescan digest unstable")
	}
}

func TestScanRejectsEmptyAndOversized(t *testing.T) {
	if _, err := ScanBytes(nil); timeline.Classify(err) != timeline.KindInvalidInput {
		t.Fatalf("empty: %v", err)
	}
	big := make([]byte, MaxScanBytes+1)
	if _, err := ScanBytes(big); timeline.Classify(err) != timeline.KindLimit {
		t.Fatalf("oversized: %v", err)
	}
}

func TestNearestKeyframeFallbackReportsDeviation(t *testing.T) {
	spec := testfixture.MP4Spec{Seed: 9, Timescale: 48000, SampleCount: 16, SampleDelta: 960,
		SampleSize: 100, KeyframeEvery: 4} // keyframes every 80 ms (4*20ms)
	data, _ := testfixture.GenerateMP4(spec)
	idx, err := ScanBytes(data)
	if err != nil {
		t.Fatal(err)
	}
	tr := idx.Tracks[0]

	// Requested cut at 100 ms: keyframes at 0,80,160... -> nearest decodable
	// boundary is 80 ms, deviation -20 ms reported explicitly.
	got, dev, err := NearestKeyframe(tr, 100_000)
	if err != nil {
		t.Fatalf("NearestKeyframe: %v", err)
	}
	if got != 80_000 {
		t.Fatalf("boundary = %d, want 80000", got)
	}
	if dev != -20_000 {
		t.Fatalf("deviation = %d, want -20000", dev)
	}

	// An exact keyframe request yields zero deviation.
	got, dev, _ = NearestKeyframe(tr, 160_000)
	if got != 160_000 || dev != 0 {
		t.Fatalf("exact boundary = %d dev %d", got, dev)
	}

	// A request beyond the last keyframe still falls back, never invents one.
	last := int64(tr.Samples[len(tr.Samples)-1].StartUS)
	got, _, err = NearestKeyframe(tr, last+1_000_000)
	if err != nil {
		t.Fatalf("beyond-end fallback: %v", err)
	}
	if got > last {
		t.Fatalf("fallback %d beyond last sample %d", got, last)
	}
}

func TestNearestKeyframeTiesPickEarlier(t *testing.T) {
	// Samples at 0,20,40,60 ms; keyframes 0 and 40. Request 20 ms is equidistant.
	spec := testfixture.MP4Spec{Seed: 3, Timescale: 48000, SampleCount: 4, SampleDelta: 960,
		SampleSize: 50, KeyframeEvery: 2}
	data, _ := testfixture.GenerateMP4(spec)
	idx, _ := ScanBytes(data)
	got, dev, err := NearestKeyframe(idx.Tracks[0], 20_000)
	if err != nil {
		t.Fatal(err)
	}
	if got != 0 {
		t.Fatalf("tie must resolve to earlier keyframe, got %d", got)
	}
	if dev != -20_000 {
		t.Fatalf("dev = %d, want -20000", dev)
	}
}
