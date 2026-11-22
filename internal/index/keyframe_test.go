package index

import (
	"testing"

	"mediad/internal/core"
	"mediad/internal/diagnose"
	"mediad/internal/ingest"
)

func track(samples []ingest.MP4Sample, timescale uint32) ingest.MP4Track {
	sync := 0
	for _, s := range samples {
		if s.Sync {
			sync++
		}
	}
	return ingest.MP4Track{TrackID: 1, Timescale: timescale, Handler: "vide", Samples: samples, SyncCount: sync, Span: core.Span{Start: 0, End: 4096}}
}

func sample(n int, dts int64, duration uint32, sync bool, offset int64) ingest.MP4Sample {
	return ingest.MP4Sample{Number: n, DTS: dts, PTS: dts, Duration: duration, Size: 10, Sync: sync, Offset: offset, Span: core.Span{Start: offset, End: offset + 10}}
}

func TestKeyframeGapsUseRealDTS(t *testing.T) {
	// VFR: GOP 1 spans 1001+1001, GOP 2 spans 1001+3003.
	tr := track([]ingest.MP4Sample{
		sample(1, 0, 1001, true, 100),
		sample(2, 1001, 1001, false, 110),
		sample(3, 2002, 1001, true, 120),
		sample(4, 3003, 3003, false, 130),
		sample(5, 6006, 1001, true, 140),
	}, 30000)
	idx, issues := Build("clip.mp4", tr, nil)
	if len(issues) != 0 {
		t.Fatalf("issues=%v", issues)
	}
	if len(idx.Keyframes) != 3 {
		t.Fatalf("keyframes=%d", len(idx.Keyframes))
	}
	gaps := idx.GapsTicks()
	if len(gaps) != 2 || gaps[0] != 2002 || gaps[1] != 4004 {
		t.Fatalf("gaps=%v", gaps)
	}
	if !idx.VFR {
		t.Fatal("expected VFR to be detected")
	}
	if idx.MinGap.Cmp(core.Rat(2002, 30000)) != 0 || idx.MaxGap.Cmp(core.Rat(4004, 30000)) != 0 {
		t.Fatalf("min=%s max=%s", idx.MinGap, idx.MaxGap)
	}
	if err := idx.Validate(4096); err != nil {
		t.Fatal(err)
	}
}

func TestKeyframeGapsNeverAveraged(t *testing.T) {
	// CFR video must report identical gaps, never a rounded average.
	var samples []ingest.MP4Sample
	for i := 0; i < 10; i++ {
		samples = append(samples, sample(i+1, int64(i)*1001, 1001, i%5 == 0, int64(200+i*10)))
	}
	idx, _ := Build("cfr.mp4", track(samples, 30000), nil)
	if idx.VFR {
		t.Fatalf("CFR misdetected as VFR: min=%s max=%s", idx.MinGap, idx.MaxGap)
	}
	for _, g := range idx.GapsTicks() {
		if g != 5005 {
			t.Fatalf("gap=%d want 5005", g)
		}
	}
}

func TestKeyframesInsideDamageAreUntrusted(t *testing.T) {
	tr := track([]ingest.MP4Sample{
		sample(1, 0, 1001, true, 100),
		sample(2, 5000, 1001, true, 950),
		sample(3, 10000, 1001, true, 1500),
	}, 30000)
	idx, _ := Build("d.mp4", tr, []core.Span{{Start: 900, End: 1000}})
	if idx.Untrusted != 1 || idx.Trusted != 2 {
		t.Fatalf("trusted=%d untrusted=%d", idx.Trusted, idx.Untrusted)
	}
	if got := idx.UntrustedKeyframes(); len(got) != 1 || got[0] != 2 {
		t.Fatalf("untrusted=%v", got)
	}
}

func TestNonIncreasingDTSIsReported(t *testing.T) {
	tr := track([]ingest.MP4Sample{
		sample(1, 0, 1001, true, 100),
		sample(2, 0, 1001, true, 110),
	}, 30000)
	_, issues := Build("x.mp4", tr, nil)
	if len(issues) != 1 || issues[0].Category != diagnose.CatMonotonic {
		t.Fatalf("issues=%v", issues)
	}
	if issues[0].Span.Start != 110 {
		t.Fatalf("span=%+v", issues[0].Span)
	}
}

func TestNoSyncSamplesReported(t *testing.T) {
	tr := track([]ingest.MP4Sample{sample(1, 0, 1001, false, 100)}, 30000)
	idx, issues := Build("y.mp4", tr, nil)
	if len(idx.Keyframes) != 0 || len(issues) != 1 {
		t.Fatalf("idx=%+v issues=%v", idx, issues)
	}
	if issues[0].Category != diagnose.CatIndexOutOfRange {
		t.Fatalf("category=%s", issues[0].Category)
	}
}

func TestValidateRejectsOutOfRangeSpan(t *testing.T) {
	idx := &Index{
		Asset: "z.mp4", Timescale: 30000, FrameCount: 2,
		Keyframes: []Keyframe{{Number: 1, DTS: 0, Offset: 4000, Span: core.Span{Start: 4000, End: 5000}}},
	}
	if err := idx.Validate(4096); err == nil {
		t.Fatal("want span validation error")
	}
}
