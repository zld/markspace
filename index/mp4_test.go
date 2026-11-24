package index

import (
	"testing"

	"example.com/media-kernel/media"
)

func TestAnalyzeMP4RoundTripAndKeyframeHits(t *testing.T) {
	data := BuildMP4Fixture(MP4FixtureConfig{
		SampleCount: 8,
		SyncSamples: []uint32{1, 4, 7},
	})
	report, err := AnalyzeMP4(data)
	if err != nil {
		t.Fatal(err)
	}
	if report.RoundTrip.Checked == 0 {
		t.Fatal("expected round-trip boxes")
	}
	if report.RoundTrip.Rate != 1 {
		for _, b := range report.RoundTrip.Boxes {
			t.Logf("box %s consistent=%v original=%d encoded=%d", b.Type, b.Consistent, b.OriginalBytes, b.EncodedBytes)
		}
		t.Fatalf("round-trip rate=%v want 1", report.RoundTrip.Rate)
	}
	if report.Index.HitRate() != 1 {
		t.Fatalf("hit rate=%v errors=%+v", report.Index.HitRate(), report.Index.Errors)
	}
	bounds := report.Index.ConfirmedBoundaries()
	if len(bounds) != 3 {
		t.Fatalf("boundaries=%d want 3", len(bounds))
	}
	if bounds[1].IntervalTicks != 120 { // samples 4-1 at 40 ticks each
		t.Fatalf("interval=%d want 120", bounds[1].IntervalTicks)
	}
}

func TestAnalyzeMP4DistinguishesMissingIndex(t *testing.T) {
	data := BuildMP4Fixture(MP4FixtureConfig{SampleCount: 5, OmitSTSS: true})
	report, err := AnalyzeMP4(data)
	if err != nil {
		t.Fatal(err)
	}
	if !report.Index.MissingIndex || len(report.Index.Errors) != 1 {
		t.Fatalf("index=%+v want one missing-index error", report.Index)
	}
	if !media.HasCode(report.Index.Errors[0], media.CodeIndexMissing) {
		t.Fatalf("error=%v want INDEX_MISSING", report.Index.Errors[0])
	}
}

func TestAnalyzeMP4DetectsCorruptIndexedKeyframe(t *testing.T) {
	data := BuildMP4Fixture(MP4FixtureConfig{
		SampleCount: 6,
		SyncSamples: []uint32{1, 4},
		Corrupt:     map[uint32]bool{4: true},
	})
	report, err := AnalyzeMP4(data)
	if err != nil {
		t.Fatal(err)
	}
	if report.Index.HitCount != 1 || report.Index.IndexedCount != 2 {
		t.Fatalf("hits=%d/%d want 1/2", report.Index.HitCount, report.Index.IndexedCount)
	}
	var found bool
	for _, e := range report.Index.Errors {
		if media.HasCode(e, media.CodeKeyframeCorrupt) && e.ByteRange != nil {
			found = true
		}
	}
	if !found {
		t.Fatalf("expected locatable keyframe corruption: %+v", report.Index.Errors)
	}
}
