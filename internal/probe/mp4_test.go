package probe

import (
	"bytes"
	"testing"

	"archive/internal/clock"
	"archive/internal/fixture"
)

func baseSpec() fixture.MP4Spec {
	return fixture.MP4Spec{
		MovieTimescale: 1000,
		Tracks: []fixture.MP4TrackSpec{{
			ID: "cam", Kind: "video",
			StoredWidth: 1920, StoredHeight: 1080,
			DisplayWidth: 1920, DisplayHeight: 1080,
			Timescale: 30000, SampleCount: 30, SampleDuration: 1001,
			Rotation: 0,
			Clap:     &fixture.MP4Clap{Width: 1440, Height: 1080},
			Colrs: []fixture.MP4Colr{
				{Primaries: 1, Transfer: 1, Matrix: 1},
				{Primaries: 9, Transfer: 16, Matrix: 9, FullRange: true},
			},
		}},
	}
}

func TestProbeMP4PreservesDeclarations(t *testing.T) {
	c, err := ProbeMP4(bytes.NewReader(fixture.BuildMP4(baseSpec())))
	if err != nil {
		t.Fatal(err)
	}
	if len(c.Tracks) != 1 {
		t.Fatalf("tracks = %d", len(c.Tracks))
	}
	tr := c.Tracks[0]
	if tr.StoredWidth != 1920 || tr.StoredHeight != 1080 {
		t.Fatalf("stored = %dx%d", tr.StoredWidth, tr.StoredHeight)
	}
	if tr.SampleCount != 30 || tr.SampleIntervalNS != 33366666 {
		t.Fatalf("samples=%d interval=%d", tr.SampleCount, tr.SampleIntervalNS)
	}
	if len(tr.Rotations) != 1 || tr.Rotations[0].Degrees != 0 {
		t.Fatalf("rotations = %+v", tr.Rotations)
	}
	if len(tr.Crops) != 1 {
		t.Fatalf("crops = %+v", tr.Crops)
	}
	if got := tr.Crops[0]; got.Left != 240 || got.Right != 1680 || got.Top != 0 || got.Bottom != 1080 {
		t.Fatalf("crop = %+v", got)
	}
	if len(tr.Colors) != 2 {
		t.Fatalf("colors preserved = %d, want 2 conflicting", len(tr.Colors))
	}
	if tr.Colors[0].Equal(tr.Colors[1]) {
		t.Fatal("distinct colours collapsed")
	}
}

func TestProbeMP4Rotation(t *testing.T) {
	spec := baseSpec()
	spec.Tracks[0].Rotation = 90
	spec.Tracks[0].Clap = nil
	spec.Tracks[0].Colrs = nil
	c, err := ProbeMP4(bytes.NewReader(fixture.BuildMP4(spec)))
	if err != nil {
		t.Fatal(err)
	}
	if c.Tracks[0].Rotations[0].Degrees != 90 {
		t.Fatalf("rotation = %d", c.Tracks[0].Rotations[0].Degrees)
	}
}

func TestProbeMP4TruncatedIsClassified(t *testing.T) {
	spec := baseSpec()
	spec.TruncateBytes = 40
	_, err := ProbeMP4(bytes.NewReader(fixture.BuildMP4(spec)))
	if got := clock.AsKind(err); got != clock.KindTruncated {
		t.Fatalf("kind = %q err = %v", got, err)
	}
	if !clock.IsFatal(err) {
		t.Fatal("truncation must be fatal")
	}
}
