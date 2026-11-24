package mediacore

import (
	"path/filepath"
	"strings"
	"testing"
)

func scanFixture(t *testing.T, path string, segDur int64) (*Container, InputDigest, *Index) {
	t.Helper()
	c, err := DemuxFile(path, DefaultDemuxOptions())
	if err != nil {
		t.Fatalf("demux: %v", err)
	}
	d, err := DigestFile(path)
	if err != nil {
		t.Fatalf("digest: %v", err)
	}
	opts := DefaultScanOptions()
	opts.SegmentDuration = segDur
	idx, err := Scan(c, d, opts)
	if err != nil {
		t.Fatalf("scan: %v", err)
	}
	return c, d, idx
}

func TestScanBuildsKeyframesAndPerfectHitRate(t *testing.T) {
	p := genFixtures(t, 17)
	_, d, idx := scanFixture(t, p.MP4, 1000)
	if idx.Digest.Content != d.Content {
		t.Fatal("index must be bound to the input digest")
	}
	v := idx.TrackByID(1)
	if v == nil {
		t.Fatal("missing video index")
	}
	if len(v.Keyframes) != 10 {
		t.Fatalf("video keyframes = %d, want 10", len(v.Keyframes))
	}
	if idx.KeyframeHitRate != 1.0 {
		t.Fatalf("keyframe hit rate = %.3f, want 1.000 (windows=%d misses=%d)",
			idx.KeyframeHitRate, idx.Plan.Windows, idx.Plan.Misses)
	}
	if len(idx.Plan.Segments) < 2 {
		t.Fatalf("segments = %d, want >= 2 for a 1.33s clip at 1s windows", len(idx.Plan.Segments))
	}
	// Every segment range must be in bounds and start on a real keyframe sample.
	for _, seg := range idx.Plan.Segments {
		if seg.First < 0 || seg.Count <= 0 || seg.First+seg.Count > v.Samples {
			t.Fatalf("segment %+v out of bounds for %d samples", seg, v.Samples)
		}
		kf, err := v.KeyframeAt(seg.Keyframe)
		if err != nil {
			t.Fatalf("segment %d keyframe index: %v", seg.Index, err)
		}
		if kf.Sample != seg.First {
			t.Fatalf("segment %d first=%d but keyframe sample=%d", seg.Index, seg.First, kf.Sample)
		}
	}
}

func TestIndexOutOfRangeIsClassified(t *testing.T) {
	p := genFixtures(t, 17)
	_, _, idx := scanFixture(t, p.MP4, 1000)
	if _, err := idx.SegmentAt(len(idx.Plan.Segments)); !IsKind(err, KindIndex) {
		t.Fatalf("SegmentAt OOB kind=%q err=%v", KindOf(err), err)
	}
	v := idx.TrackByID(1)
	if _, err := v.KeyframeAt(-1); !IsKind(err, KindIndex) {
		t.Fatalf("KeyframeAt(-1) kind=%q", KindOf(err))
	}
	if _, err := v.KeyframeAt(len(v.Keyframes)); !IsKind(err, KindIndex) {
		t.Fatalf("KeyframeAt OOB kind=%q", KindOf(err))
	}
}

func TestScanRejectsTimestampRegression(t *testing.T) {
	c := &Container{MovieTimescale: 1000, Tracks: []Track{{
		ID: 1, Kind: KindVideoTrack, Timescale: 90000,
		Samples: []SampleInfo{
			{Index: 0, DTS: 0, Size: 10, Sync: true},
			{Index: 1, DTS: 0, Size: 10, Sync: true}, // equal -> not strictly increasing
		},
	}}}
	opts := ScanOptions{Map: TrackMapConfig{Rules: []TrackRule{{Kind: KindVideoTrack}}}}
	if _, err := Scan(c, InputDigest{Content: "x"}, opts); !IsKind(err, KindTimestamp) {
		t.Fatalf("expected KindTimestamp, got kind=%q err=%v", KindOf(err), err)
	}
}

func TestScanRejectsTrackWithoutKeyframes(t *testing.T) {
	c := &Container{MovieTimescale: 1000, Tracks: []Track{{
		ID: 1, Kind: KindVideoTrack, Timescale: 90000,
		Samples: []SampleInfo{
			{Index: 0, DTS: 0, Size: 10, Sync: false},
			{Index: 1, DTS: 3000, Size: 10, Sync: false},
		},
	}}}
	opts := ScanOptions{Map: TrackMapConfig{Rules: []TrackRule{{Kind: KindVideoTrack}}}}
	if _, err := Scan(c, InputDigest{Content: "x"}, opts); !IsKind(err, KindIndex) {
		t.Fatalf("expected KindIndex for no keyframes, got %v", err)
	}
}

func TestDigestChangesOnMutation(t *testing.T) {
	p := genFixtures(t, 17)
	a, err := DigestFile(p.MP4)
	if err != nil {
		t.Fatal(err)
	}
	b, err := DigestFile(p.Mutated)
	if err != nil {
		t.Fatal(err)
	}
	if a.Content == b.Content || a.Size != b.Size {
		t.Fatalf("mutation must change content digest (a=%s size=%d, b=%s size=%d)",
			short(a.Content), a.Size, short(b.Content), b.Size)
	}
	if err := a.Verify(p.MP4); err != nil {
		t.Fatalf("original digest should verify: %v", err)
	}
	if err := a.Verify(p.Mutated); err == nil {
		t.Fatal("stale digest must not verify mutated bytes")
	}
}

func TestCacheHitThenDigestChangeRebuild(t *testing.T) {
	p := genFixtures(t, 17)
	dir := filepath.Join(t.TempDir(), "cache")
	cache := NewIndexCache(dir)

	build := func(path string) func() (*Index, error) {
		return func() (*Index, error) {
			c, err := DemuxFile(path, DefaultDemuxOptions())
			if err != nil {
				return nil, err
			}
			d, err := DigestFile(path)
			if err != nil {
				return nil, err
			}
			return Scan(c, d, DefaultScanOptions())
		}
	}

	first, err := cache.Load(p.MP4, build(p.MP4))
	if err != nil {
		t.Fatal(err)
	}
	if !first.Rebuilt || first.Reason != "cache missing" {
		t.Fatalf("first load rebuilt=%t reason=%q", first.Rebuilt, first.Reason)
	}
	second, err := cache.Load(p.MP4, build(p.MP4))
	if err != nil {
		t.Fatal(err)
	}
	if second.Rebuilt || second.Reason != "cache hit" {
		t.Fatalf("second load rebuilt=%t reason=%q, want cache hit", second.Rebuilt, second.Reason)
	}

	// Same cache directory, different (mutated) input bytes: must detect the
	// digest mismatch and rebuild, never serve the stale index.
	mut, err := cache.Load(p.Mutated, build(p.Mutated))
	if err != nil {
		t.Fatal(err)
	}
	if !mut.Rebuilt || !strings.Contains(mut.Reason, "digest changed") {
		t.Fatalf("mutated load rebuilt=%t reason=%q, want digest changed", mut.Rebuilt, mut.Reason)
	}
	if mut.Index.Digest.Content == first.Index.Digest.Content {
		t.Fatal("rebuilt index must be bound to the new digest")
	}

	// Reloading the mutated path now hits the freshly stored entry.
	again, err := cache.Load(p.Mutated, build(p.Mutated))
	if err != nil {
		t.Fatal(err)
	}
	if again.Rebuilt {
		t.Fatal("reload of mutated input should be a cache hit")
	}
}

func TestCanonicalJSONDeterministic(t *testing.T) {
	p := genFixtures(t, 17)
	_, _, idx1 := scanFixture(t, p.MP4, 1000)
	_, _, idx2 := scanFixture(t, p.MP4, 1000)
	b1, err := idx1.CanonicalJSON()
	if err != nil {
		t.Fatal(err)
	}
	b2, _ := idx2.CanonicalJSON()
	if string(b1) != string(b2) {
		t.Fatal("canonical index JSON must be identical across scans")
	}
}
