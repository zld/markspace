package render_test

import (
	"os"
	"testing"

	"mediapack/internal/fixture"
	"mediapack/internal/ingest"
	"mediapack/internal/render"
	"mediapack/internal/timeline"
)

func writeFile(p string, b []byte) error { return os.WriteFile(p, b, 0o600) }

func parsed(t *testing.T, s fixture.Spec) *ingest.Container {
	t.Helper()
	dir := t.TempDir()
	p := dir + "/s.mp4"
	if err := writeFile(p, fixture.BuildMP4(s)); err != nil {
		t.Fatal(err)
	}
	c, err := ingest.ParseMP4(p)
	if err != nil {
		t.Fatal(err)
	}
	return c
}

func baseSpec() fixture.Spec {
	return fixture.Spec{
		Video: fixture.VideoSpec{
			TrackID: 1, Timescale: 24000, Width: 16, Height: 12,
			Rotation: 90,
			Crop: &fixture.CropSpec{
				CleanWidthN: 12, CleanWidthD: 1,
				CleanHeightN: 8, CleanHeightD: 1,
			},
			Colors: []fixture.ColorSpec{
				{Type: "nclx", Primaries: 1, Transfer: 1, Matrix: 1},
			},
			Delta: 1001, Frames: 5,
		},
	}
}

func layer() render.LayerConfig {
	return render.LayerConfig{Name: "low", Bitrate: 400, Width: 8, Height: 6}
}

func TestRenderFrameCountTicksAndHashes(t *testing.T) {
	c := parsed(t, baseSpec())
	tl, err := timeline.Compose(c)
	if err != nil {
		t.Fatal(err)
	}
	digest, err := render.SummaryDigest(c.Tracks)
	if err != nil {
		t.Fatal(err)
	}
	r := render.NewRenderer(digest)
	frames, err := r.RenderLayer(layer(), tl)
	if err != nil {
		t.Fatal(err)
	}
	if len(frames) != 5 {
		t.Fatalf("frames = %d, want 5", len(frames))
	}
	for i, f := range frames {
		if f.Index != i || f.SourceTrack != 0 || f.SourceSample != i {
			t.Fatalf("frame provenance wrong: %+v", f)
		}
		if f.PixelFormat != render.PixelFormat || f.Width != 8 || f.Height != 6 {
			t.Fatalf("frame descriptor wrong: %+v", f)
		}
		if len(f.Hash) != 64 || f.InputDigest != digest {
			t.Fatalf("hash/digest wrong: %+v", f)
		}
		if f.Tick < 0 {
			t.Fatalf("negative tick: %+v", f)
		}
	}
	// Ticks are non-decreasing across samples.
	for i := 1; i < len(frames); i++ {
		if frames[i-1].Tick > frames[i].Tick {
			t.Fatal("ticks must be non-decreasing")
		}
	}
}

func TestRenderDeterministic(t *testing.T) {
	build := func() []render.Frame {
		c := parsed(t, baseSpec())
		tl, _ := timeline.Compose(c)
		digest, _ := render.SummaryDigest(c.Tracks)
		frames, err := render.NewRenderer(digest).RenderLayer(layer(), tl)
		if err != nil {
			t.Fatal(err)
		}
		return frames
	}
	a, b := build(), build()
	if len(a) != len(b) {
		t.Fatal("frame count differs across reruns")
	}
	for i := range a {
		if a[i].Hash != b[i].Hash {
			t.Fatalf("frame %d hash differs across identical reruns", i)
		}
		if a[i].Tick != b[i].Tick {
			t.Fatalf("frame %d tick differs across reruns", i)
		}
	}
}

func TestRotationChangesPixels(t *testing.T) {
	s := baseSpec()
	c0 := parsed(t, s)
	tl0, _ := timeline.Compose(c0)
	d0, _ := render.SummaryDigest(c0.Tracks)
	f0, err := render.NewRenderer(d0).RenderLayer(layer(), tl0)
	if err != nil {
		t.Fatal(err)
	}

	s.Video.Rotation = 0
	c1 := parsed(t, s)
	tl1, _ := timeline.Compose(c1)
	d1, _ := render.SummaryDigest(c1.Tracks)
	f1, err := render.NewRenderer(d1).RenderLayer(layer(), tl1)
	if err != nil {
		t.Fatal(err)
	}
	if f0[0].Hash == f1[0].Hash {
		t.Fatal("rotation must change exported pixels")
	}
}

func TestCropChangesPixelsAndFallbackKeepsFullAperture(t *testing.T) {
	s := baseSpec()
	cCrop := parsed(t, s)
	tl, _ := timeline.Compose(cCrop)
	d, _ := render.SummaryDigest(cCrop.Tracks)
	cropped, err := render.NewRenderer(d).RenderLayer(layer(), tl)
	if err != nil {
		t.Fatal(err)
	}

	noCrop := baseSpec()
	noCrop.Video.Crop = nil
	cFull := parsed(t, noCrop)
	tlF, _ := timeline.Compose(cFull)
	dF, _ := render.SummaryDigest(cFull.Tracks)
	full, err := render.NewRenderer(dF).RenderLayer(layer(), tlF)
	if err != nil {
		t.Fatal(err)
	}
	if cropped[0].Hash == full[0].Hash {
		t.Fatal("legal crop must change exported pixels")
	}
}

func TestTwoLayersProduceIndependentGeometry(t *testing.T) {
	c := parsed(t, baseSpec())
	tl, _ := timeline.Compose(c)
	d, _ := render.SummaryDigest(c.Tracks)
	r := render.NewRenderer(d)
	low, err := r.RenderLayer(render.LayerConfig{Name: "low", Width: 8, Height: 6}, tl)
	if err != nil {
		t.Fatal(err)
	}
	high, err := r.RenderLayer(render.LayerConfig{Name: "high", Width: 16, Height: 12}, tl)
	if err != nil {
		t.Fatal(err)
	}
	if len(low) != len(high) || low[0].Hash == high[0].Hash {
		t.Fatal("layers must export the same timeline at distinct geometry")
	}
	if high[0].Width != 16 || high[0].Layer != "high" {
		t.Fatalf("high layer descriptor wrong: %+v", high[0])
	}
}
