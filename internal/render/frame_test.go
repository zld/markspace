package render

import (
	"bytes"
	"testing"

	"mediaaudit/internal/model"
)

type fakeReader struct{ data []byte }

func (f *fakeReader) ReadAt(p []byte, off int64) (int, error) {
	if off >= int64(len(f.data)) {
		return 0, ioEOF{}
	}
	n := copy(p, f.data[off:])
	return n, nil
}

type ioEOF struct{}

func (ioEOF) Error() string { return "EOF" }

func samples() []SampleRef {
	return []SampleRef{
		{Index: 0, Offset: 0, Size: 4, Start: model.RatFromInt(0), Duration: model.MustRat(1, 2), Keyframe: true, Origin: "index"},
		{Index: 1, Offset: 4, Size: 4, Start: model.MustRat(1, 2), Duration: model.MustRat(1, 2), Keyframe: false, Origin: "index"},
		{Index: 2, Offset: 8, Size: 4, Start: model.RatFromInt(1), Duration: model.MustRat(1, 2), Keyframe: true, Origin: "index"},
	}
}

func TestFindSample(t *testing.T) {
	ss := samples()
	cases := []struct {
		t    string
		want int
	}{
		{"-1/10", -1}, {"0", 0}, {"1/4", 0}, {"1/2", 1}, {"3/4", 1}, {"1", 2}, {"100", 2},
	}
	for _, c := range cases {
		tv, _ := model.ParseRat(c.t)
		if got := FindSample(ss, tv); got != c.want {
			t.Fatalf("FindSample(%s)=%d want %d", c.t, got, c.want)
		}
	}
	if FindSample(nil, model.RatFromInt(0)) != -1 {
		t.Fatal("empty sample list must return -1")
	}
}

func TestSamplingPointsFromTimeline(t *testing.T) {
	items := []model.Span{
		{Start: model.MustRat(1, 2), End: model.MustRat(5, 2)},
		{Start: model.RatFromInt(0), End: model.RatFromInt(2)},
	}
	opt := DefaultOptions(model.MustRat(1, 2))
	pts := SamplingPoints(items, opt)
	// Window is [0,2.5) with a 0.5 s cadence: 0, .5, 1, 1.5, 2.
	if len(pts) != 5 {
		t.Fatalf("points=%v", pts)
	}
	if !pts[0].IsZero() || !pts[4].Equal(model.RatFromInt(2)) {
		t.Fatalf("bounds %s..%s", pts[0], pts[4])
	}
}

func TestSamplingPointsInvalidInterval(t *testing.T) {
	if got := SamplingPoints([]model.Span{{End: model.RatFromInt(5)}}, Options{Interval: model.RatFromInt(0)}); got != nil {
		t.Fatal("zero interval must produce no points")
	}
}

func TestRenderAtProducesStableHashes(t *testing.T) {
	r := &fakeReader{data: []byte("abcdefghijkl")}
	opt := DefaultOptions(model.MustRat(1, 2))
	tr := model.Track{ID: "v1", Kind: model.KindVideo}
	a := RenderAt(r, tr, samples(), model.RatFromInt(0), opt)
	b := RenderAt(r, tr, samples(), model.RatFromInt(0), opt)
	if !a.Ok() || a.PixelHash != b.PixelHash || a.ByteHash != b.ByteHash {
		t.Fatalf("hashes unstable: %+v %+v", a, b)
	}
	if a.SampleIdx != 0 || !a.Keyframe {
		t.Fatalf("sample ref %+v", a)
	}
	c := RenderAt(r, tr, samples(), model.MustRat(3, 4), opt)
	if c.SampleIdx != 1 || c.PixelHash == a.PixelHash {
		t.Fatalf("different sample produced same hash: %+v", c)
	}
}

func TestRenderAtMissingSampleIsError(t *testing.T) {
	r := &fakeReader{data: []byte("abcdefghijkl")}
	opt := DefaultOptions(model.MustRat(1, 2))
	rec := RenderAt(r, model.Track{ID: "v1", Kind: model.KindVideo}, samples(), model.RatFromInt(-1), opt)
	if rec.Error == nil || rec.Error.Code != model.CodeRenderNoSample || rec.PixelHash != "" {
		t.Fatalf("no-sample record must not fake a frame: %+v", rec)
	}
}

func TestRenderAtShortReadIsError(t *testing.T) {
	// Only 6 bytes exist but sample 2 needs bytes 8..12.
	r := &fakeReader{data: []byte("abcdef")}
	opt := DefaultOptions(model.MustRat(1, 2))
	rec := RenderAt(r, model.Track{ID: "v1", Kind: model.KindVideo}, samples(), model.RatFromInt(1), opt)
	if rec.Error == nil || rec.Error.Code != model.CodeSampleRead {
		t.Fatalf("short read not classified: %+v", rec)
	}
	if rec.PixelHash != "" || rec.ByteHash != "" {
		t.Fatal("failed read must not emit hashes")
	}
}

func TestPixelHashDependsOnDimensions(t *testing.T) {
	r := []byte{1, 2, 3, 4}
	if PixelHash(r, 2, 2) == PixelHash(r, 4, 1) {
		t.Fatal("dimensions must be mixed into the hash")
	}
	if !bytes.Equal(RasterFromPayload([]byte{9}, 2, 2), []byte{9, 9, 9, 9}) {
		t.Fatal("raster expansion")
	}
	if RasterFromPayload(nil, 2, 2) != nil {
		t.Fatal("empty payload must produce no raster")
	}
}

func TestRenderBatchKeepsFailures(t *testing.T) {
	r := &fakeReader{data: []byte("abcdefghijkl")}
	opt := DefaultOptions(model.MustRat(1, 2))
	pts := []model.Rat{model.RatFromInt(0), model.RatFromInt(-5), model.RatFromInt(1)}
	recs := RenderBatch(r, model.Track{ID: "v1", Kind: model.KindVideo}, samples(), pts, opt)
	if len(recs) != 3 || recs[0].Error != nil || recs[1].Error == nil || recs[2].Error != nil {
		t.Fatalf("records %+v", recs)
	}
}

func TestRenderAtGapResolvesToPreviousSample(t *testing.T) {
	// Samples 0..0.5 and 1.0..1.5: a point at 0.75 lies in a gap.
	ss := []SampleRef{
		{Index: 0, Offset: 0, Size: 2, Start: model.RatFromInt(0), Duration: model.MustRat(1, 2)},
		{Index: 1, Offset: 2, Size: 2, Start: model.RatFromInt(1), Duration: model.MustRat(1, 2)},
	}
	opt := DefaultOptions(model.MustRat(1, 4))
	rec := RenderAt(&fakeReader{data: []byte{1, 2, 3, 4}}, model.Track{ID: "v", Kind: model.KindVideo},
		ss, model.MustRat(3, 4), opt)
	// The record must be honest about which sample it used; it does not claim
	// the gap has a frame of its own.
	if rec.SampleIdx != 0 || rec.Error != nil {
		t.Fatalf("gap resolved to %+v", rec)
	}
}

func TestRenderZeroRasterDoesNotPanic(t *testing.T) {
	opt := Options{Interval: model.RatFromInt(1), Width: 0, Height: 0}
	rec := RenderAt(&fakeReader{data: []byte("abcdefghijkl")}, model.Track{ID: "v", Kind: model.KindVideo},
		samples(), model.RatFromInt(0), opt)
	if rec.Error != nil {
		t.Fatalf("zero raster must still produce a record: %v", rec.Error)
	}
	if rec.PixelHash != "" {
		t.Fatal("a zero-size raster must not produce a pixel hash")
	}
}
