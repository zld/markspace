package media_test

import (
	"path/filepath"
	"testing"

	"continuity/fixture"
	"continuity/media"
)

// TestProbeIndexRoundtrip asserts the roundtrip parse consistency rate is
// at least 0.99 and the keyframe index hit rate is exactly 1.000 over a
// batch of generated inputs.
func TestProbeIndexRoundtrip(t *testing.T) {
	dir := t.TempDir()
	const n = 8
	var consistent, hits, total int
	for i := 0; i < n; i++ {
		p := filepath.Join(dir, "v.mp4")
		if err := fixture.Generate(p, fixture.ValidSpec()); err != nil {
			t.Fatal(err)
		}
		c1, err := media.Probe(p)
		if err != nil {
			t.Fatal(err)
		}
		c2, err := media.Probe(p)
		if err != nil {
			t.Fatal(err)
		}
		total++
		if c1.FileSHA256 == c2.FileSHA256 && len(c1.Tracks) == len(c2.Tracks) &&
			c1.Tracks[0].DurationNanos == c2.Tracks[0].DurationNanos {
			consistent++
		}
		ix, err := media.BuildIndex(c1.VideoTrack())
		if err != nil {
			t.Fatal(err)
		}
		// Every 2 s cut point must hit a keyframe exactly.
		for cut := int64(0); cut < c1.VideoTrack().DurationNanos; cut += 2_000_000_000 {
			if ix.HasKeyframeAt(cut) {
				hits++
			}
			total++
		}
	}
	rate := float64(consistent) / 8.0
	if rate < 0.99 {
		t.Fatalf("roundtrip consistency %.3f < 0.99", rate)
	}
	hitRate := float64(hits) / float64(total-8)
	if hitRate != 1.0 {
		t.Fatalf("keyframe hit rate %.3f != 1.000", hitRate)
	}
}

func TestIndexKeyframeLookup(t *testing.T) {
	p := filepath.Join(t.TempDir(), "v.mp4")
	if err := fixture.Generate(p, fixture.ValidSpec()); err != nil {
		t.Fatal(err)
	}
	c, err := media.Probe(p)
	if err != nil {
		t.Fatal(err)
	}
	ix, err := media.BuildIndex(c.VideoTrack())
	if err != nil {
		t.Fatal(err)
	}
	if !ix.HasKeyframeAt(2_000_000_000) {
		t.Fatal("expected keyframe at 2s")
	}
	if ix.HasKeyframeAt(1_500_000_000) {
		t.Fatal("unexpected keyframe at 1.5s")
	}
	at, ok := ix.KeyframeAtOrBefore(3_000_000_000)
	if !ok || at != 2_000_000_000 {
		t.Fatalf("keyframe at-or-before 3s = %d, want 2s", at)
	}
	from, to := ix.SampleRange(2_000_000_000, 4_000_000_000)
	if from != 60 || to != 120 {
		t.Fatalf("sample range [%d,%d), want [60,120)", from, to)
	}
}
