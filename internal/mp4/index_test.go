package mp4

import (
	"bytes"
	"testing"

	"mediaaudit/internal/fixtures"
	"mediaaudit/internal/model"
)

func spec() fixtures.MP4Spec {
	return fixtures.MP4Spec{
		TrackID: 1, Timescale: 30000, Width: 64, Height: 36,
		Sizes:           []uint32{10, 20, 30, 40, 50, 60},
		Deltas:          []uint32{1000, 1000, 1000, 1000, 1000, 1000},
		Keyframes:       []int{1, 4},
		SamplesPerChunk: 2,
	}
}

func parseSpec(t *testing.T, s fixtures.MP4Spec, opts Options) (*Index, error) {
	t.Helper()
	data, err := fixtures.MP4(s)
	if err != nil {
		t.Fatal(err)
	}
	ix, es := Parse(bytes.NewReader(data), "test.mp4", opts)
	if e := es.FirstFatal(); e != nil {
		return ix, e
	}
	return ix, nil
}

func TestParseSamplesAndOffsets(t *testing.T) {
	ix, err := parseSpec(t, spec(), Options{FallbackScan: true})
	if err != nil {
		t.Fatal(err)
	}
	tr, ok := ix.VideoTrack()
	if !ok {
		t.Fatal("no video track")
	}
	if tr.SampleCount != 6 || tr.Keyframes != 2 || tr.KeyframeOrigin != OriginIndex {
		t.Fatalf("track info %+v", tr)
	}
	samples := ix.Samples(1)
	if len(samples) != 6 {
		t.Fatalf("samples=%d", len(samples))
	}
	// Sample 3 starts after samples 0..2 in a 2-per-chunk layout.
	if samples[0].Offset == 0 || samples[3].Offset <= samples[2].Offset {
		t.Fatalf("offsets not increasing: %d %d %d", samples[2].Offset, samples[3].Offset, samples[0].Offset)
	}
	if samples[1].Offset-samples[0].Offset != 10 {
		t.Fatalf("offsets do not advance by size: %d", samples[1].Offset-samples[0].Offset)
	}
	if !samples[0].Keyframe || !samples[3].Keyframe || samples[1].Keyframe {
		t.Fatalf("keyframe flags wrong: %+v", samples)
	}
	if samples[0].Duration.String() != "1/30" {
		t.Fatalf("duration %s", samples[0].Duration)
	}
}

func TestParseCttsPresentationTime(t *testing.T) {
	s := spec()
	s.Ctts = []int64{3000, 0, 0, 0, 0, 0}
	ix, err := parseSpec(t, s, Options{FallbackScan: true})
	if err != nil {
		t.Fatal(err)
	}
	samples := ix.Samples(1)
	// ctts offset for sample 0 is 3000 ticks at 30000 Hz = 0.1 s.
	if !samples[0].PresentTime.Equal(model.MustRat(1, 10)) || !samples[0].DecodeTime.IsZero() {
		t.Fatalf("pts %s dts %s", samples[0].PresentTime, samples[0].DecodeTime)
	}
	if !samples[1].PresentTime.Equal(samples[1].DecodeTime) {
		t.Fatalf("sample 1 pts %s dts %s", samples[1].PresentTime, samples[1].DecodeTime)
	}
}

func TestOutOfRangeKeyframeFallsBack(t *testing.T) {
	s := spec()
	s.OutOfRangeKeyframe = true
	ix, err := parseSpec(t, s, Options{FallbackScan: true})
	if err != nil {
		t.Fatal(err)
	}
	tr, _ := ix.VideoTrack()
	if tr.KeyframeOrigin != OriginScan {
		t.Fatalf("want scan fallback, got %s", tr.KeyframeOrigin)
	}
	if tr.FallbackReason != "stss_sample_out_of_range" || tr.BadIndexSample != 9 || tr.BadIndexEntry != 3 {
		t.Fatalf("fallback reason %q entry %d sample %d", tr.FallbackReason, tr.BadIndexEntry, tr.BadIndexSample)
	}
	if tr.BadIndexOffset == 0 {
		t.Fatal("damaged entry must carry a byte offset")
	}
	for _, smp := range ix.Samples(1) {
		if smp.Keyframe && smp.Origin != OriginScan {
			t.Fatalf("sample %d origin %s", smp.Index, smp.Origin)
		}
	}
}

func TestMissingStssFallsBack(t *testing.T) {
	s := spec()
	s.MissingStss = true
	ix, err := parseSpec(t, s, Options{FallbackScan: true})
	if err != nil {
		t.Fatal(err)
	}
	tr, _ := ix.VideoTrack()
	if tr.KeyframeOrigin != OriginScan || tr.FallbackReason != "stss_missing" {
		t.Fatalf("want scan due to missing stss, got %s/%s", tr.KeyframeOrigin, tr.FallbackReason)
	}
	// Without a scan the reader must still succeed but report no keyframes.
	ix2, err := parseSpec(t, s, Options{FallbackScan: false})
	if err != nil {
		t.Fatal(err)
	}
	tr2, _ := ix2.VideoTrack()
	if tr2.KeyframeOrigin != OriginNone || tr2.Keyframes != 0 {
		t.Fatalf("disabled fallback: %+v", tr2)
	}
}

func TestTruncatedSampleIsAddressable(t *testing.T) {
	// Truncation shortens mdat but the tables still describe every sample;
	// the reader reports addresses and the render layer detects the short read.
	s := spec()
	s.TruncateSample = 10
	data, err := fixtures.MP4(s)
	if err != nil {
		t.Fatal(err)
	}
	ix, es := Parse(bytes.NewReader(data), "trunc.mp4", Options{FallbackScan: true})
	if es.FirstFatal() != nil {
		t.Fatalf("metadata must still parse: %v", es)
	}
	samples := ix.Samples(1)
	if samples[len(samples)-1].Size != 60 {
		t.Fatalf("last sample size %d", samples[len(samples)-1].Size)
	}
}

func TestCorruptTopLevelReportedNotFatal(t *testing.T) {
	// Garbage is reported against this file only; the batch continues so
	// other tracks can still be audited.
	for _, in := range [][]byte{[]byte("not an mp4 at all"), nil} {
		ix, es := Parse(bytes.NewReader(in), "bad.mp4", Options{})
		if len(ix.Tracks) != 0 {
			t.Fatal("garbage must yield no tracks")
		}
		if len(es) != 1 || es[0].Code != model.CodeIndexCorrupt {
			t.Fatalf("diagnostics %v", es)
		}
		if es[0].Code.Fatal() || !es[0].Recoverable() {
			t.Fatal("damage must be recoverable for the rest of the batch")
		}
	}
}
