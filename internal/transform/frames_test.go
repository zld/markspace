package transform_test

import (
	"testing"

	"mediacore/internal/fixture"
	"mediacore/internal/ingest"
	"mediacore/internal/timeline"
	"mediacore/internal/transform"
)

// mapPayloads resolves sample byte ranges against the injected stream.
type mapPayloads struct {
	data []byte
}

func (m mapPayloads) SampleBytes(s timeline.Sample) ([]byte, error) {
	return m.data[s.FileOffset : s.FileOffset+s.ByteLength], nil
}

func framesFor(t *testing.T, seed int64, kinds ...fixture.FaultKind) (*transform.FrameSet, []timeline.Sample) {
	t.Helper()
	injected, err := fixture.Inject(seed, 2, 24, kinds)
	if err != nil {
		t.Fatal(err)
	}
	r, f := ingest.NewReader("s.fmp4", injected.Data, ingest.DefaultLimits())
	if f != nil {
		t.Fatal(f)
	}
	info, f := ingest.ParseInit(r)
	if f != nil {
		t.Fatal(f)
	}
	idx, _ := ingest.ScanIndex(r, info.MoovOffset+info.MoovSize)
	res := r.ScanFragmentsBounded(info.MoovOffset+info.MoovSize, idx)
	tracks, _ := timeline.BuildTracks([]*ingest.InitInfo{info}, res.Fragments, 90000)
	_, repaired := transform.Repair(tracks, transform.RepairOptions{})
	var samples []timeline.Sample
	for _, rt := range repaired {
		if !rt.Mergeable {
			continue
		}
		samples = append(samples, rt.Samples...)
	}
	set, err := transform.BuildFrames(samples, mapPayloads{data: injected.Data}, transform.FrameOptions{Seed: seed, Timescale: 90000})
	if err != nil {
		t.Fatal(err)
	}
	return set, samples
}

func TestFramesAreDeterministicForSameSeed(t *testing.T) {
	a, _ := framesFor(t, 41)
	b, _ := framesFor(t, 41)
	if a.Digest != b.Digest {
		t.Fatalf("digest differs: %s vs %s", a.Digest, b.Digest)
	}
	if len(a.Frames) != len(b.Frames) {
		t.Fatalf("frame count differs")
	}
	for i := range a.Frames {
		if a.Frames[i].PixelSHA != b.Frames[i].PixelSHA {
			t.Fatalf("frame %d pixel digest differs", i)
		}
		if a.Frames[i].SourceOffset != b.Frames[i].SourceOffset {
			t.Fatalf("frame %d source differs", i)
		}
	}
}

func TestFramesChangeWithSeed(t *testing.T) {
	a, _ := framesFor(t, 41)
	b, _ := framesFor(t, 42)
	if a.Digest == b.Digest {
		t.Fatal("different seeds produced identical frame digests")
	}
}

func TestFramesCarryFullProvenance(t *testing.T) {
	set, samples := framesFor(t, 41)
	if len(set.Frames) != len(samples) {
		t.Fatalf("frames=%d samples=%d", len(set.Frames), len(samples))
	}
	for i, f := range set.Frames {
		if f.Number != i {
			t.Fatalf("frame %d numbered %d", i, f.Number)
		}
		if f.SourceOffset <= 0 || f.SourceSize <= 0 || f.SourceFile == "" {
			t.Fatalf("frame %d lacks source provenance: %+v", i, f)
		}
		if f.PixelSHA == "" || f.SourceSHA == "" {
			t.Fatalf("frame %d lacks digests", i)
		}
		if f.Width <= 0 || f.Height <= 0 {
			t.Fatalf("frame %d lacks raster size", i)
		}
	}
	if set.Digest == "" {
		t.Fatal("frame set has no root digest")
	}
}

func TestFramesAreStableAcrossFaultRepair(t *testing.T) {
	// Repair must not make the exported sequence order non-deterministic:
	// two runs of the same faulted input agree exactly.
	a, _ := framesFor(t, 41, fixture.FaultTfdtRegression, fixture.FaultDuplicate)
	b, _ := framesFor(t, 41, fixture.FaultTfdtRegression, fixture.FaultDuplicate)
	if a.Digest != b.Digest {
		t.Fatal("repair introduced nondeterminism in frames")
	}
	for i := 1; i < len(a.Frames); i++ {
		if a.Frames[i].TimelineTime < a.Frames[i-1].TimelineTime {
			t.Fatalf("frame %d time regressed", i)
		}
	}
}
